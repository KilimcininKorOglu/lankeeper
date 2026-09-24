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

	req, err := c.newRequest(method, params)
	if err != nil {
		return nil, err
	}
	_ = c.conn.SetDeadline(c.applyDeadline(ctx, req))

	// Encode and Decode block with no cancellation of their own, so
	// wire the context to the socket: pulling the deadline into the
	// past unblocks whichever one is in flight.
	stop := make(chan struct{})
	defer close(stop)
	go cancelOnDone(ctx, c.conn, stop)

	resp, err := c.roundTrip(ctx, req)
	if err != nil {
		return nil, err
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

// newRequest builds the next JSON-RPC request with a fresh ID.
func (c *Client) newRequest(method string, params any) (*Request, error) {
	var rawParams json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("marshal params: %w", err)
		}
		rawParams = b
	}
	return &Request{
		JSONRPC: "2.0",
		Method:  method,
		Params:  rawParams,
		ID:      c.nextID.Add(1),
	}, nil
}

// applyDeadline returns the socket deadline for the call and records the
// caller's remaining budget on req.
func (c *Client) applyDeadline(ctx context.Context, req *Request) time.Time {
	deadline, ok := ctx.Deadline()
	if !ok {
		// callTimeout is deliberately not sent when the caller set no
		// deadline: it is this client's liveness guard, not a budget
		// anyone asked for, and forwarding it would stretch every
		// command to it.
		return time.Now().Add(c.callTimeout)
	}
	// Tell the agent how long the caller allowed. Without this the
	// agent sees no deadline and falls back to its own 30 s, which
	// silently overrules a caller that granted more.
	if remaining := time.Until(deadline); remaining > 0 {
		req.TimeoutMS = remaining.Milliseconds()
	}
	return deadline
}

// cancelOnDone pulls the socket deadline into the past when ctx ends,
// which unblocks an in-flight Encode or Decode. conn is passed by value
// because the error paths in roundTrip call c.close, which nils the
// field. SetDeadline on a closed connection just returns an error, which
// is not interesting here.
func cancelOnDone(ctx context.Context, conn net.Conn, stop <-chan struct{}) {
	select {
	case <-ctx.Done():
		_ = conn.SetDeadline(time.Now())
	case <-stop:
	}
}

// roundTrip sends req and reads one response.
//
// A failed or cancelled call must drop the connection, not just return.
// The stream is a sequential request/response pipe and Decode does not
// match on response ID, so leaving an unread reply behind would hand it
// to the next caller.
func (c *Client) roundTrip(ctx context.Context, req *Request) (*Response, error) {
	if err := c.enc.Encode(req); err != nil {
		return nil, c.failCall(ctx, "send request", err)
	}
	var resp Response
	if err := c.dec.Decode(&resp); err != nil {
		return nil, c.failCall(ctx, "read response", err)
	}
	return &resp, nil
}

// failCall closes the connection and reports the context error in
// preference to the I/O error it caused.
func (c *Client) failCall(ctx context.Context, op string, err error) error {
	_ = c.close()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return fmt.Errorf("%s: %w", op, err)
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
