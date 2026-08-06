package netutil

import "fmt"

// AgentError marks a failure that came back across the agent IPC
// boundary.
//
// The root agent embeds the failed process's stderr in the error it
// returns, and every layer above wraps that string without changing it,
// so by the time a web handler sees it the text carries raw nft,
// wg-quick, easyrsa and openvpn output, exact command names, and
// internal temp-file naming. That detail belongs in the journal, not in
// a browser response, and the two-process split exists precisely to keep
// agent-side detail agent-side.
//
// Callers keep the full error for logging and use errors.As to tell an
// agent failure from one their own validation produced, which they can
// safely show.
type AgentError struct {
	// Op is the RPC method, for example "exec.run".
	Op string
	// Target is the command or path the operation acted on.
	Target string
	Err    error
}

func (e *AgentError) Error() string {
	return fmt.Sprintf("agent %s %s: %v", e.Op, e.Target, e.Err)
}

func (e *AgentError) Unwrap() error { return e.Err }
