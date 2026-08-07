package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	ID      any             `json:"id"`
	// TimeoutMS carries the caller's remaining time budget. Remaining
	// duration rather than an absolute deadline: both processes share a
	// clock, but a duration is immune to an NTP step landing between
	// send and receive. Omitted when the caller set no deadline, in
	// which case the handler applies its own default.
	TimeoutMS int64 `json:"timeoutMs,omitempty"`
}

type Response struct {
	JSONRPC string      `json:"jsonrpc"`
	Result  any         `json:"result,omitempty"`
	Error   *RPCError   `json:"error,omitempty"`
	ID      any         `json:"id"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type Handler func(ctx context.Context, params json.RawMessage) (any, error)

// defaultServiceIdentity is the account deploy/install.sh creates for
// the unprivileged serve process. It is the only peer allowed to reach
// the agent besides root.
const defaultServiceIdentity = "lankeeper"

// errPeerCredUnsupported marks platforms where the kernel does not
// expose the peer's credentials to us. Returned by peerUID on non-Linux
// builds.
var errPeerCredUnsupported = errors.New("peer credentials unsupported on this platform")

// defaultMaxFrameBytes bounds one JSON-RPC request. Request.Params is a
// json.RawMessage, so the decoder buffers the whole value before
// dispatch, inside the root process. The largest legitimate request is
// a file.write of a rendered system config, which runs to kilobytes, so
// a mebibyte is a comfortable ceiling.
const defaultMaxFrameBytes int64 = 1 << 20

// defaultFrameTimeout bounds how long a request may take to arrive once
// its first byte has been read. It deliberately does not apply to an
// idle connection: the client keeps one open between calls, and closing
// it would surface as a failed call rather than a transparent redial.
const defaultFrameTimeout = 30 * time.Second

// defaultMaxConns bounds how many connections the agent serves at once.
//
// A connection is served serially, so this is also the bound on
// concurrent privileged subprocesses: the agent runs as root and every
// exec.run forks a command, which on a small appliance is the resource
// worth protecting. That serialisation was previously incidental to how
// the one caller happens to be written (a single connection behind a
// mutex) rather than anything the agent enforced, so a pooled client or
// a second caller would have removed the bound without touching this
// file.
//
// The shipped caller uses exactly one connection. Sixteen leaves room
// for a pooled client and for a root operator debugging alongside it.
const defaultMaxConns = 16

// errAgentBusy is what a refused connection is told before it is
// closed. Writing it costs one small frame and turns an unexplained EOF
// into a diagnosable message.
const errAgentBusy = "agent: too many concurrent connections"

type Server struct {
	socketPath   string
	serviceUser  string
	serviceGroup string
	listener     net.Listener
	mu           sync.RWMutex
	handlers     map[string]Handler

	// Overridable so tests can drive the limits without sending a
	// mebibyte or waiting half a minute.
	maxFrameBytes int64
	frameTimeout  time.Duration

	// connSlots holds one token per in-flight connection. Its capacity
	// is the limit; a token is taken at accept and returned when the
	// connection handler exits.
	connSlots chan struct{}
}

func NewServer(socketPath string) *Server {
	return NewServerWithIdentity(socketPath, defaultServiceIdentity, defaultServiceIdentity)
}

// NewServerWithIdentity sets the account and group permitted to use the
// socket. The agent runs as root and executes whitelisted commands on
// behalf of the caller, so this pair is the privilege boundary.
func NewServerWithIdentity(socketPath, serviceUser, serviceGroup string) *Server {
	return &Server{
		socketPath:    socketPath,
		serviceUser:   serviceUser,
		serviceGroup:  serviceGroup,
		handlers:      make(map[string]Handler),
		maxFrameBytes: defaultMaxFrameBytes,
		frameTimeout:  defaultFrameTimeout,
		connSlots:     make(chan struct{}, defaultMaxConns),
	}
}

func (s *Server) Register(method string, handler Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[method] = handler
}

func (s *Server) GetHandler(method string) Handler {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.handlers[method]
}

func (s *Server) Serve(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(s.socketPath), 0o755); err != nil {
		return fmt.Errorf("create socket dir: %w", err)
	}

	_ = os.Remove(s.socketPath)

	var err error
	s.listener, err = net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("listen unix: %w", err)
	}

	if err := s.restrictSocket(); err != nil {
		return err
	}

	log.Printf("agent listening on %s", s.socketPath)

	go func() {
		<-ctx.Done()
		_ = s.listener.Close()
	}()

	for {
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				log.Printf("accept error: %v", err)
				continue
			}
		}
		// Refuse rather than queue. Queueing an unbounded number of
		// waiting connections would rebuild the problem behind the
		// limit, and the client redials on its next call, so a refusal
		// is recoverable in a way an exhausted appliance is not.
		select {
		case s.connSlots <- struct{}{}:
		default:
			log.Printf("agent: refusing connection, %d already open", cap(s.connSlots))
			refuseConn(conn)
			continue
		}

		go func() {
			defer func() { <-s.connSlots }()
			s.handleConn(ctx, conn)
		}()
	}
}

// refuseConn tells the peer why before hanging up. The caller has not
// sent its request yet, so the reply carries a null id, which JSON-RPC
// defines for an error raised before the id is known.
func refuseConn(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_ = json.NewEncoder(conn).Encode(&Response{
		JSONRPC: "2.0",
		Error:   &RPCError{Code: -32000, Message: errAgentBusy},
	})
}

// restrictSocket confines the socket to root and the service group.
//
// The agent executes whitelisted commands as root on behalf of whoever
// connects, so the socket mode is the privilege boundary itself. When
// the service group cannot be resolved, or the agent is not running as
// root and therefore cannot hand the socket to that group, the socket
// is left owner-only. That fails closed: the serve process cannot
// connect and says so, which is recoverable, whereas a permissive mode
// silently hands root to every local account.
func (s *Server) restrictSocket() error {
	gid, err := lookupGID(s.serviceGroup)
	if err == nil && os.Geteuid() == 0 {
		if err := os.Chown(s.socketPath, 0, gid); err != nil {
			return fmt.Errorf("chown socket to group %s: %w", s.serviceGroup, err)
		}
		if err := os.Chmod(s.socketPath, 0o660); err != nil {
			return fmt.Errorf("chmod socket: %w", err)
		}
		return nil
	}

	if err != nil {
		log.Printf("agent: group %q not found (%v); socket restricted to its owner, "+
			"the serve process will not be able to connect", s.serviceGroup, err)
	} else {
		log.Printf("agent: not running as root; socket restricted to its owner")
	}
	if err := os.Chmod(s.socketPath, 0o600); err != nil {
		return fmt.Errorf("chmod socket: %w", err)
	}
	return nil
}

// lookupGID resolves a group name to its numeric ID.
func lookupGID(name string) (int, error) {
	g, err := user.LookupGroup(name)
	if err != nil {
		return 0, err
	}
	gid, err := strconv.Atoi(g.Gid)
	if err != nil {
		return 0, fmt.Errorf("parse gid %q: %w", g.Gid, err)
	}
	return gid, nil
}

// authorizePeer reports whether the connected process may drive the
// agent. Root and the service account are allowed; everything else is
// refused. On platforms without peer-credential support the socket mode
// set by restrictSocket remains the only control.
func (s *Server) authorizePeer(conn net.Conn) error {
	uid, err := peerUID(conn)
	if errors.Is(err, errPeerCredUnsupported) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read peer credentials: %w", err)
	}
	if uid == 0 {
		return nil
	}
	if u, lookupErr := user.Lookup(s.serviceUser); lookupErr == nil && u.Uid == strconv.FormatUint(uint64(uid), 10) {
		return nil
	}
	return fmt.Errorf("uid %d is neither root nor %s", uid, s.serviceUser)
}

func (s *Server) Close() {
	if s.listener != nil {
		_ = s.listener.Close()
	}
	_ = os.Remove(s.socketPath)
}

func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	defer func() { _ = conn.Close() }()

	if err := s.authorizePeer(conn); err != nil {
		log.Printf("agent: rejected connection: %v", err)
		return
	}

	fr := &frameReader{
		conn:    conn,
		max:     s.maxFrameBytes,
		timeout: s.frameTimeout,
	}
	dec := json.NewDecoder(fr)
	enc := json.NewEncoder(conn)

	for {
		var req Request
		if err := dec.Decode(&req); err != nil {
			if errors.Is(err, errFrameTooLarge) {
				log.Printf("agent: rejected oversized request frame (limit %d bytes)", s.maxFrameBytes)
			}
			return
		}
		fr.endFrame()

		resp := s.dispatch(ctx, &req)
		if err := enc.Encode(resp); err != nil {
			log.Printf("encode response error: %v", err)
			return
		}
	}
}

// maxRequestedTimeout caps what a caller may ask for. The peer is
// authenticated, but a compromised serve process should not be able to
// tie up an agent goroutine for an arbitrary length of time. Comfortably
// above the longest budget any real caller sets.
const maxRequestedTimeout = 15 * time.Minute

// requestedTimeout converts the wire value into a duration, clamping it
// to the ceiling. A zero or negative value means the caller expressed no
// preference and the handler's own default applies.
func requestedTimeout(ms int64) time.Duration {
	if ms <= 0 {
		return 0
	}
	d := time.Duration(ms) * time.Millisecond
	if d > maxRequestedTimeout {
		return maxRequestedTimeout
	}
	return d
}

var errFrameTooLarge = errors.New("agent: request frame exceeds the size limit")

// frameReader bounds a single JSON-RPC request without breaking the
// long-lived connection the client keeps open between calls.
//
// A plain io.LimitReader would cap the connection's whole lifetime and
// kill a healthy client after enough requests. A plain idle deadline
// would close a connection that is merely waiting for the next call,
// which the client surfaces as a failed call rather than redialling.
//
// So the budget resets after every decoded request, and the read
// deadline is armed only once a frame has started arriving. An idle
// connection carries no deadline at all; a half-sent one cannot hold
// its goroutine past timeout.
type frameReader struct {
	conn    net.Conn
	max     int64
	timeout time.Duration
	read    int64
	armed   bool
}

func (f *frameReader) Read(p []byte) (int, error) {
	if f.read >= f.max {
		return 0, errFrameTooLarge
	}
	if remaining := f.max - f.read; int64(len(p)) > remaining {
		p = p[:remaining]
	}

	n, err := f.conn.Read(p)
	if n > 0 && !f.armed {
		// First byte of a frame: give it a bounded window to finish.
		f.armed = true
		_ = f.conn.SetReadDeadline(time.Now().Add(f.timeout))
	}
	f.read += int64(n)
	if f.read >= f.max && err == nil {
		return n, errFrameTooLarge
	}
	return n, err
}

// endFrame is called after a request decodes. It returns the connection
// to the idle state: full budget, no deadline.
func (f *frameReader) endFrame() {
	f.read = 0
	if f.armed {
		f.armed = false
		_ = f.conn.SetReadDeadline(time.Time{})
	}
}

func (s *Server) dispatch(ctx context.Context, req *Request) *Response {
	if req.JSONRPC != "2.0" {
		return &Response{
			JSONRPC: "2.0",
			Error:   &RPCError{Code: -32600, Message: "invalid JSON-RPC version"},
			ID:      req.ID,
		}
	}

	s.mu.RLock()
	handler, ok := s.handlers[req.Method]
	s.mu.RUnlock()

	if !ok {
		return &Response{
			JSONRPC: "2.0",
			Error:   &RPCError{Code: -32601, Message: fmt.Sprintf("method not found: %s", req.Method)},
			ID:      req.ID,
		}
	}

	// Give the handler the caller's budget. Without this the context
	// reaching a handler never carried a deadline, so opExecRun always
	// substituted its own 30 s and no caller could ask for more,
	// however long the command legitimately needed.
	if d := requestedTimeout(req.TimeoutMS); d > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}

	result, err := handler(ctx, req.Params)
	if err != nil {
		return &Response{
			JSONRPC: "2.0",
			Error:   &RPCError{Code: -32000, Message: err.Error()},
			ID:      req.ID,
		}
	}

	return &Response{
		JSONRPC: "2.0",
		Result:  result,
		ID:      req.ID,
	}
}
