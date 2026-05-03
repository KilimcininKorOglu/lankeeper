package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"os/user"
	"syscall"

	"github.com/KilimcininKorOglu/home-router/internal/agent"
)

func runAgent() error {
	fs := flag.NewFlagSet("agent", flag.ExitOnError)
	socketPath := fs.String("socket", "/run/home-router/agent.sock", "UDS listen path")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}

	u, err := user.Current()
	if err != nil {
		return fmt.Errorf("failed to get current user: %w", err)
	}
	if u.Uid != "0" {
		return fmt.Errorf("agent must run as root (current uid=%s)", u.Uid)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	srv := agent.NewServer(*socketPath)
	agent.RegisterBuiltinOps(srv)

	log.Printf("home-router agent starting (socket=%s)", *socketPath)

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve(ctx)
	}()

	select {
	case <-ctx.Done():
		log.Println("agent shutting down...")
		srv.Close()
		return nil
	case err := <-errCh:
		return err
	}
}
