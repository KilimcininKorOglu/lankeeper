package services

import "testing"

// The passphrase is the only protection of an archive stored off-box,
// so a short one falls to offline guessing against the scrypt KDF.
func TestValidateBackupPassphraseEnforcesTheFloor(t *testing.T) {
	for _, p := range []string{"", "x", "elevenchars"} {
		if ValidateBackupPassphrase(p) == nil {
			t.Errorf("passphrase %q was accepted", p)
		}
	}
	for _, p := range []string{"twelve chars", "şifreşifreşi"} {
		if err := ValidateBackupPassphrase(p); err != nil {
			t.Errorf("passphrase %q was refused: %v", p, err)
		}
	}
}
