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
)

type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	ID      any             `json:"id"`
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

type Server struct {
	socketPath   string
	serviceUser  string
	serviceGroup string
	listener     net.Listener
	mu           sync.RWMutex
	handlers     map[string]Handler
}

func NewServer(socketPath string) *Server {
	return NewServerWithIdentity(socketPath, defaultServiceIdentity, defaultServiceIdentity)
}

// NewServerWithIdentity sets the account and group permitted to use the
// socket. The agent runs as root and executes whitelisted commands on
// behalf of the caller, so this pair is the privilege boundary.
func NewServerWithIdentity(socketPath, serviceUser, serviceGroup string) *Server {
	return &Server{
		socketPath:   socketPath,
		serviceUser:  serviceUser,
		serviceGroup: serviceGroup,
		handlers:     make(map[string]Handler),
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
		go s.handleConn(ctx, conn)
	}
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

	dec := json.NewDecoder(conn)
	enc := json.NewEncoder(conn)

	for {
		var req Request
		if err := dec.Decode(&req); err != nil {
			return
		}

		resp := s.dispatch(ctx, &req)
		if err := enc.Encode(resp); err != nil {
			log.Printf("encode response error: %v", err)
			return
		}
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
