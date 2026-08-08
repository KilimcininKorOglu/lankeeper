package buildsys_test

import (
	"regexp"
	"strings"
	"testing"
)

// The runtime package set is written out three times, in three files
// that are edited on different occasions:
//
//   - deploy/install.sh runs on an existing Debian and installs from the
//     network.
//   - deploy/iso/build-iso.sh downloads the .deb files into the offline
//     ISO.
//   - deploy/iso/post-install.sh installs them inside the target during
//     an unattended install.
//
// The three are not identical on purpose: the ISO also carries the
// Debian Standard task, dbus, openssh-server and htop. What has to hold
// is one direction, that anything install.sh needs is present in the
// other two. A package added to install.sh alone produces an ISO that
// installs cleanly and then runs a router missing a daemon, which is
// only discovered on the hardware.
func TestISOListsCoverEveryRuntimePackage(t *testing.T) {
	installed := packagesFromAptInstall(t, "../deploy/install.sh")
	if len(installed) < 10 {
		t.Fatalf("parsed %d packages from install.sh; the apt-get install block was not found as expected", len(installed))
	}

	iso := packagesFromAptInstall(t, "../deploy/iso/post-install.sh")
	builder := readAsset(t, "../deploy/iso/build-iso.sh")
	lankeeperList := betweenMarkers(t, builder, "LANKEEPER_PACKAGES=(", ")")

	for _, pkg := range installed {
		if !contains(iso, pkg) {
			t.Errorf("%q is installed by deploy/install.sh but missing from deploy/iso/post-install.sh, so an ISO install runs without it", pkg)
		}
		if !strings.Contains(lankeeperList, pkg) {
			t.Errorf("%q is installed by deploy/install.sh but missing from LANKEEPER_PACKAGES in deploy/iso/build-iso.sh, so its .deb is never fetched into the offline ISO", pkg)
		}
	}
}

// TestCheckInstallationVerifiesInstalledCommands keeps the post-install
// check honest. It listed qrencode, a package nothing installs any more,
// so a correct install reported a missing dependency.
func TestCheckInstallationVerifiesInstalledCommands(t *testing.T) {
	script := readAsset(t, "../deploy/install.sh")
	installed := packagesFromAptInstall(t, "../deploy/install.sh")

	loop := regexp.MustCompile(`for cmd in ([^;]+); do`).FindStringSubmatch(script)
	if loop == nil {
		t.Fatal("the check_installation command loop was not found")
	}

	// Command names and package names differ often enough that a
	// one-to-one mapping would be wrong. Only the ones that match a
	// package name are checked, which is enough to catch a command left
	// behind after its package was dropped.
	pkgNames := map[string]bool{}
	for _, p := range installed {
		pkgNames[p] = true
	}
	for _, cmd := range strings.Fields(loop[1]) {
		if looksLikePackageName(cmd) && !pkgNames[cmd] {
			t.Errorf("check_installation looks for %q but no package by that name is installed; a correct install reports it missing", cmd)
		}
	}
}

// packagesFromAptInstall pulls the package names out of the single
// multi-line `apt-get install` invocation in a script.
func packagesFromAptInstall(t *testing.T, path string) []string {
	t.Helper()
	script := readAsset(t, path)

	idx := strings.Index(script, "apt-get install")
	if idx < 0 {
		t.Fatalf("%s has no apt-get install block", path)
	}
	rest := script[idx:]

	var fields []string
	for line := range strings.SplitSeq(rest, "\n") {
		trimmed := strings.TrimSpace(line)
		continues := strings.HasSuffix(trimmed, "\\")
		trimmed = strings.TrimSuffix(trimmed, "\\")
		for _, f := range strings.Fields(trimmed) {
			if strings.HasPrefix(f, "-") || f == "apt-get" || f == "install" {
				continue
			}
			fields = append(fields, f)
		}
		if !continues {
			break
		}
	}
	return fields
}

func betweenMarkers(t *testing.T, src, start, end string) string {
	t.Helper()
	i := strings.Index(src, start)
	if i < 0 {
		t.Fatalf("marker %q not found", start)
	}
	rest := src[i+len(start):]
	j := strings.Index(rest, end)
	if j < 0 {
		t.Fatalf("closing marker %q not found after %q", end, start)
	}
	return rest[:j]
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// looksLikePackageName filters out the command names that deliberately
// differ from their package (chronyc from chrony, nft from nftables,
// wg from wireguard-tools, and so on).
func looksLikePackageName(cmd string) bool {
	switch cmd {
	case "pppd", "nft", "wg", "chronyc", "smbcontrol", "smartctl":
		return false
	}
	return true
}
