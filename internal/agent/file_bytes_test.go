package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// encoding/json replaces invalid UTF-8 in a string with U+FFFD, so a
// binary or Latin-1 file has to cross the wire as bytes and arrive
// unchanged in both directions.
func TestFileContentRoundTripsNonUTF8Bytes(t *testing.T) {
	body := []byte{'c', 'a', 'f', 0xe9, '\n', 0xff, 0x00}
	path := filepath.Join("/tmp", "lankeeper-bytes-"+filepath.Base(t.TempDir()))
	t.Cleanup(func() { _ = os.Remove(path) })

	raw, _ := json.Marshal(FileWriteParams{Path: path, ContentBytes: body, Mode: 0o600})
	if _, err := opFileWrite(context.Background(), raw); err != nil {
		t.Fatalf("write: %v", err)
	}
	onDisk, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(onDisk, body) {
		t.Fatalf("written %q (%v), want %q", onDisk, err, body)
	}

	raw, _ = json.Marshal(FileReadParams{Path: path})
	out, err := opFileRead(context.Background(), raw)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	wire, _ := json.Marshal(out)
	var got FileContent
	if err := json.Unmarshal(wire, &got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes(), body) {
		t.Errorf("read back %q, want %q", got.Bytes(), body)
	}
}
