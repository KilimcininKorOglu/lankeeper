//go:build !linux

package agent

import "net"

// peerUID is unavailable outside Linux. The product deploys only to
// Debian, so this path exists for local development builds; there the
// socket mode set by restrictSocket is the sole access control.
func peerUID(net.Conn) (uint32, error) {
	return 0, errPeerCredUnsupported
}
