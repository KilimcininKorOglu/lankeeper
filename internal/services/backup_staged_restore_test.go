package services

import (
	"archive/tar"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

func addContent(t *testing.T, tw *tar.Writer, name, content string) {
	t.Helper()
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
}

func importWithRefusingAgent(t *testing.T, build func(tw *tar.Writer)) ([]string, error) {
	t.Helper()
	root := t.TempDir()
	origExtra := backupExtraDirs
	backupExtraDirs = []string{filepath.Join(root, "unbound")}
	t.Cleanup(func() { backupExtraDirs = origExtra })
	agent := &refusingAgent{}
	netutil.SetAgentClient(agent)
	t.Cleanup(func() { netutil.SetAgentClient(nil) })

	archive := filepath.Join(root, "backup.tar.gz")
	writeArchive(t, archive, build)
	err := NewBackupService(filepath.Join(root, "lankeeper")).Import(context.Background(), archive, "")
	return agent.calls, err
}

// serve refuses an invalid config at start, and the web UI is the only
// interface that could fix it, so such an archive must not be installed.
func TestImportRefusesAnInvalidConfigBeforeWritingAnything(t *testing.T) {
	calls, err := importWithRefusingAgent(t, func(tw *tar.Writer) {
		addContent(t, tw, "unbound/unbound.conf", "server:\n")
		addContent(t, tw, "lankeeper/router.yaml", "system:\n  hostname: \"\"\n")
	})
	if err == nil || !strings.Contains(err.Error(), "router.yaml") {
		t.Fatalf("import error = %v, want a router.yaml refusal", err)
	}
	if len(calls) != 0 {
		t.Fatalf("files were written before the config was checked: %v", calls)
	}
}

// A member refused late in the archive must not leave the earlier ones
// installed over the live files.
func TestImportWritesNothingWhenALaterMemberIsRefused(t *testing.T) {
	calls, err := importWithRefusingAgent(t, func(tw *tar.Writer) {
		addContent(t, tw, "lankeeper/router.yaml", validRouterYAML(t))
		addContent(t, tw, "unbound/unbound.conf", "server:\n")
		addContent(t, tw, "/etc/cron.d/x", "* * * * * root id\n")
	})
	if err == nil {
		t.Fatal("an archive with an absolute member was accepted")
	}
	if len(calls) != 0 {
		t.Fatalf("members before the refused one were written: %v", calls)
	}
}
