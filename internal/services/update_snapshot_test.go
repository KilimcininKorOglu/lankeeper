package services

import (
	"context"
	"strings"
	"testing"
)

// TestPreUpdateSnapshotIsWrittenWhereTheServiceAccountCannotPlantALink
// is the regression test. Root's tar wrote the snapshot, and chmod then
// followed it, at a name built from the release tag inside
// /var/lib/lankeeper/backups, which the installers hand to the service
// account. That account could create the name first as a symlink to any
// file, and root would overwrite and re-mode the target.
func TestPreUpdateSnapshotIsWrittenWhereTheServiceAccountCannotPlantALink(t *testing.T) {
	svc, agent := newPendingUpdateService(t)
	svc.backup = NewBackupService(t.TempDir())

	got := svc.snapshotConfig(context.Background(), "v1.2.3")

	const want = "/var/backups/lankeeper-pre-update-v1.2.3.tar.gz"
	if got != want {
		t.Errorf("snapshot = %q, want %q", got, want)
	}
	if n := agent.count("tar czf " + want + " "); n != 1 {
		t.Errorf("tar wrote %s %d times, want 1; calls: %q", want, n, agent.calls)
	}
	for _, c := range agent.calls {
		if strings.Contains(c, "/var/lib/lankeeper/backups") {
			t.Errorf("a root command still targets the service account's directory: %q", c)
		}
	}
}
