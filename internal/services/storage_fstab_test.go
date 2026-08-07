package services

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// fstabAgent serves /etc/fstab from memory so the composition can be
// observed, and can be told to fail the read.
type fstabAgent struct {
	mu       sync.Mutex
	content  string
	readErr  bool
	writeErr bool
	writes   int
}

func (a *fstabAgent) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}

	switch method {
	case "file.read":
		if a.readErr {
			return nil, errors.New("agent unavailable")
		}
		return json.Marshal(struct {
			Content string `json:"content"`
		}{Content: a.content})
	case "file.write":
		if a.writeErr {
			return nil, errors.New("read-only filesystem")
		}
		var p struct {
			Content string `json:"content"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		a.content = p.Content
		a.writes++
		return []byte(`{"status":"ok"}`), nil
	}
	return []byte(`{}`), nil
}

func (a *fstabAgent) body() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.content
}

const realFstab = "" +
	"UUID=aaaa-bbbb / ext4 errors=remount-ro 0 1\n" +
	"UUID=cccc-dddd /boot/efi vfat umask=0077 0 1\n" +
	"/swapfile none swap sw 0 0\n"

func useFstabAgent(t *testing.T, a *fstabAgent) {
	t.Helper()
	netutil.SetAgentClient(a)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })
}

// TestANewlineCannotAppendASecondEntry is the regression test. The line
// was composed with fmt.Sprintf and written straight through the root
// agent, so a newline in the device or the mount point ended the record
// early and appended another one, which the system honours at the next
// boot.
//
// Mutates the process-global agent client, so no t.Parallel here.
func TestANewlineCannotAppendASecondEntry(t *testing.T) {
	agent := &fstabAgent{content: realFstab}
	useFstabAgent(t, agent)

	err := appendFstabEntry("/dev/sda1", "/srv/nas\n/dev/sdb1 /root ext4 defaults 0 0", "ext4")
	if err == nil {
		t.Fatal("a mount point carrying a newline was accepted")
	}
	if !strings.Contains(err.Error(), "not a mount point") {
		t.Errorf("unexpected error: %v", err)
	}
	if agent.body() != realFstab {
		t.Errorf("fstab was modified by a rejected entry:\n%s", agent.body())
	}
}

// TestWhitespaceCannotSplitAField covers the other separator: fstab
// fields are whitespace-separated, so a space shifts every field after
// it and the mount point becomes the filesystem type.
func TestWhitespaceCannotSplitAField(t *testing.T) {
	agent := &fstabAgent{content: realFstab}
	useFstabAgent(t, agent)

	cases := map[string][2]string{
		"space in the mount point": {"/dev/sda1", "/srv/my nas"},
		"tab in the mount point":   {"/dev/sda1", "/srv\tnas"},
		"space in the device":      {"/dev/sda1 x", "/srv/nas"},
		"newline in the device":    {"/dev/sda1\nevil", "/srv/nas"},
		"not a device path":        {"sda1", "/srv/nas"},
		"relative mount point":     {"/dev/sda1", "srv/nas"},
		"traversal":                {"/dev/sda1", "/srv/../etc"},
		"empty device":             {"", "/srv/nas"},
		"empty mount point":        {"/dev/sda1", ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if err := appendFstabEntry(tc[0], tc[1], "ext4"); err == nil {
				t.Errorf("appendFstabEntry(%q, %q) was accepted", tc[0], tc[1])
			}
		})
	}
	if agent.body() != realFstab {
		t.Errorf("fstab was modified:\n%s", agent.body())
	}
}

// TestAValidEntryIsAppended keeps the guard from blocking the work it
// protects, and confirms the existing records survive.
func TestAValidEntryIsAppended(t *testing.T) {
	agent := &fstabAgent{content: realFstab}
	useFstabAgent(t, agent)

	if err := appendFstabEntry("/dev/md0", "/srv/nas", "ext4"); err != nil {
		t.Fatalf("a valid entry was refused: %v", err)
	}

	got := agent.body()
	if !strings.Contains(got, "/dev/md0 /srv/nas ext4 defaults 0 2") {
		t.Errorf("the entry was not appended:\n%s", got)
	}
	for _, keep := range []string{"UUID=aaaa-bbbb / ext4", "/swapfile none swap"} {
		if !strings.Contains(got, keep) {
			t.Errorf("an existing record was lost: %q missing from:\n%s", keep, got)
		}
	}
}

// TestAnUnreadableFstabIsNotOverwritten is the more serious half, and it
// is not the injection. The read error was discarded, so a failed read
// left the existing content empty and the composition replaced the whole
// of /etc/fstab with the single new line, dropping the root filesystem
// entry and everything else.
func TestAnUnreadableFstabIsNotOverwritten(t *testing.T) {
	agent := &fstabAgent{content: realFstab, readErr: true}
	useFstabAgent(t, agent)

	err := appendFstabEntry("/dev/md0", "/srv/nas", "ext4")
	if err == nil {
		t.Fatal("an unreadable fstab was treated as an empty one")
	}
	if !strings.Contains(err.Error(), "read fstab") {
		t.Errorf("unexpected error: %v", err)
	}
	if agent.writes != 0 {
		t.Error("fstab was written after the read failed")
	}
}

// TestAFailedWriteIsReported covers the discarded write error. The
// callers were told the mount succeeded, so a filesystem the operator
// had just created came back unmounted at the next boot with nothing to
// explain it.
func TestAFailedWriteIsReported(t *testing.T) {
	agent := &fstabAgent{content: realFstab, writeErr: true}
	useFstabAgent(t, agent)

	err := appendFstabEntry("/dev/md0", "/srv/nas", "ext4")
	if err == nil {
		t.Fatal("a failed fstab write was reported as success")
	}
	if !strings.Contains(err.Error(), "write fstab") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestAnExistingEntryIsNotDuplicated keeps the idempotence the original
// had.
func TestAnExistingEntryIsNotDuplicated(t *testing.T) {
	agent := &fstabAgent{content: realFstab}
	useFstabAgent(t, agent)

	for i := 0; i < 3; i++ {
		if err := appendFstabEntry("/dev/md0", "/srv/nas", "ext4"); err != nil {
			t.Fatalf("call %d failed: %v", i, err)
		}
	}
	if got := strings.Count(agent.body(), "/dev/md0 /srv/nas ext4"); got != 1 {
		t.Errorf("the entry appears %d times, want 1:\n%s", got, agent.body())
	}
	if agent.writes != 1 {
		t.Errorf("fstab was rewritten %d times for one entry", agent.writes)
	}
}

// TestTheCallersPropagateTheFailure ties the fix to the callers. Both
// used to discard the result, so a refused or failed entry still
// reported a successful mount.
func TestTheCallersPropagateTheFailure(t *testing.T) {
	agent := &fstabAgent{content: realFstab, writeErr: true}
	useFstabAgent(t, agent)

	svc := NewStorageService(&config.Config{})

	if err := svc.FormatAndMount(context.Background(), "/dev/sdz1", "/srv/nas"); err == nil {
		t.Error("FormatAndMount reported success with an unwritable fstab")
	}
	if err := svc.CreateRAID(context.Background(), 1,
		[]string{"/dev/sdy1", "/dev/sdz1"}, "/srv/nas"); err == nil {
		t.Error("CreateRAID reported success with an unwritable fstab")
	}
}
