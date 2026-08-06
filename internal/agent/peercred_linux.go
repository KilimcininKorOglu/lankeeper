//go:build linux

package agent

import (
	"fmt"
	"net"
	"syscall"
)

// peerUID returns the effective user ID of the process on the other end
// of a Unix socket, read from the kernel via SO_PEERCRED. The value is
// supplied by the kernel at connect time, so it cannot be forged by the
// peer.
func peerUID(conn net.Conn) (uint32, error) {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, fmt.Errorf("connection is %T, not a unix socket", conn)
	}

	raw, err := unixConn.SyscallConn()
	if err != nil {
		return 0, fmt.Errorf("raw conn: %w", err)
	}

	var (
		cred    *syscall.Ucred
		credErr error
	)
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return 0, fmt.Errorf("control: %w", err)
	}
	if credErr != nil {
		return 0, fmt.Errorf("getsockopt SO_PEERCRED: %w", credErr)
	}

	return cred.Uid, nil
}
