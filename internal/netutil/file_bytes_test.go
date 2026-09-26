package netutil

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
)

// bytesAgent keeps what file.write sent and returns it from file.read,
// passing both through JSON as the socket does.
type bytesAgent struct{ stored json.RawMessage }

func (a *bytesAgent) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	var p struct {
		Content      string `json:"content"`
		ContentBytes []byte `json:"contentBytes"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if method == "file.write" {
		a.stored, err = json.Marshal(p)
		return []byte(`{}`), err
	}
	return a.stored, nil
}

func TestWriteAndReadKeepNonUTF8Bytes(t *testing.T) {
	SetAgentClient(&bytesAgent{})
	t.Cleanup(func() { SetAgentClient(nil) })

	for _, body := range [][]byte{[]byte("plain text\n"), {'c', 'a', 'f', 0xe9, 0xff}} {
		if err := WriteFile("/etc/lankeeper/x", body, 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := ReadFile("/etc/lankeeper/x")
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, body) {
			t.Errorf("round trip turned %q into %q", body, got)
		}
	}
}
