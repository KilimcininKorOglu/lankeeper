package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

type Client struct {
	socketPath string
	// dialTimeout bounds connecting to a local Unix socket, which is
	// either immediate or hopeless.
	dialTimeout time.Duration
	// callTimeout applies only when the caller's context carries no
	// deadline of its own. It is a liveness guard, not a policy
	// ceiling: it exists so a wedged agent cannot hold mu forever.
	// Responsiveness comes from context cancellation, which Call now
	// honours, so this can be generous enough for the slowest
	// privileged command (easyrsa gen-dh and build-ca) instead of
	// cutting it off at an arbitrary point.
	callTimeout time.Duration
	mu          sync.Mutex
	conn        net.Conn
	enc         *json.Encoder
	dec         *json.Decoder
	nextID      atomic.Int64
}

func NewClient(socketPath string) *Client {
	return &Client{
		socketPath:  socketPath,
		dialTimeout: 10 * time.Second,
		callTimeout: 10 * time.Minute,
	}
}

func (c *Client) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	// Never open a connection or start privileged work for a context
	// that is already done.
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Waiting for the lock can take as long as the call ahead of us,
	// so re-check before committing to the work.
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if err := c.ensureConn(ctx); err != nil {
		return nil, err
	}

	id := c.nextID.Add(1)

	var rawParams json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("marshal params: %w", err)
		}
		rawParams = b
	}

	req := &Request{
		JSONRPC: "2.0",
		Method:  method,
		Params:  rawParams,
		ID:      id,
	}

	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(c.callTimeout)
	}
	_ = c.conn.SetDeadline(deadline)

	// Encode and Decode block with no cancellation of their own, so
	// wire the context to the socket: pulling the deadline into the
	// past unblocks whichever one is in flight.
	//
	// conn is a local copy because the error paths below call c.close,
	// which nils the field. SetDeadline on a closed connection just
	// returns an error, which is not interesting here.
	conn := c.conn
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.SetDeadline(time.Now())
		case <-stop:
		}
	}()

	// A cancelled call must drop the connection, not just return. The
	// stream is a sequential request/response pipe and Decode does not
	// match on response ID, so leaving an unread reply behind would
	// hand it to the next caller. Both error paths already close.
	if err := c.enc.Encode(req); err != nil {
		_ = c.close()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("send request: %w", err)
	}

	var resp Response
	if err := c.dec.Decode(&resp); err != nil {
		_ = c.close()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("rpc error %d: %s", resp.Error.Code, resp.Error.Message)
	}

	raw, err := json.Marshal(resp.Result)
	if err != nil {
		return nil, fmt.Errorf("marshal result: %w", err)
	}

	return raw, nil
}

func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.close()
}

func (c *Client) ensureConn(ctx context.Context) error {
	if c.conn != nil {
		return nil
	}

	d := net.Dialer{Timeout: c.dialTimeout}
	conn, err := d.DialContext(ctx, "unix", c.socketPath)
	if err != nil {
		return fmt.Errorf("dial agent: %w", err)
	}

	c.conn = conn
	c.enc = json.NewEncoder(conn)
	c.dec = json.NewDecoder(conn)
	return nil
}

func (c *Client) close() error {
	if c.conn == nil {
		return nil
	}
	err := c.conn.Close()
	c.conn = nil
	c.enc = nil
	c.dec = nil
	return err
}
