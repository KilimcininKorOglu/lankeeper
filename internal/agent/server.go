package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
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

type Server struct {
	socketPath string
	listener   net.Listener
	mu         sync.RWMutex
	handlers   map[string]Handler
}

func NewServer(socketPath string) *Server {
	return &Server{
		socketPath: socketPath,
		handlers:   make(map[string]Handler),
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

	os.Remove(s.socketPath)

	var err error
	s.listener, err = net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("listen unix: %w", err)
	}

	if err := os.Chmod(s.socketPath, 0o666); err != nil {
		return fmt.Errorf("chmod socket: %w", err)
	}

	log.Printf("agent listening on %s", s.socketPath)

	go func() {
		<-ctx.Done()
		s.listener.Close()
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

func (s *Server) Close() {
	if s.listener != nil {
		s.listener.Close()
	}
	os.Remove(s.socketPath)
}

func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()

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
