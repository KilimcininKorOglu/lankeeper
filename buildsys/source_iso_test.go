package buildsys_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// sourceImages returns the default source image paths with the Debian
// point release expanded.
func sourceImages(t *testing.T, mk string) map[string]string {
	t.Helper()
	release := makeVar(t, mk, "DEBIAN_RELEASE")
	out := map[string]string{}
	for _, arch := range []string{"amd64", "arm64"} {
		raw := makeVar(t, mk, "DEBIAN_"+strings.ToUpper(arch)+"_ISO")
		out[arch] = strings.ReplaceAll(raw, "$(DEBIAN_RELEASE)", release)
	}
	return out
}

// build-iso.sh refuses a source image whose digest the checksum file
// does not list, so the images the Makefile downloads by default must be
// ones it lists, or every fresh build fetches an image it then rejects.
func TestTheFetchedSourceImagesAreOnesTheBuildAccepts(t *testing.T) {
	mk, err := os.ReadFile("../Makefile")
	if err != nil {
		t.Fatal(err)
	}
	sums, err := os.ReadFile("../deploy/iso/debian-images.sha512")
	if err != nil {
		t.Fatal(err)
	}
	for arch, path := range sourceImages(t, string(mk)) {
		if !strings.Contains(string(sums), "  "+filepath.Base(path)+"\n") {
			t.Errorf("%s image %s is not listed in debian-images.sha512", arch, filepath.Base(path))
		}
	}
}

// A missing source image is fetched over HTTPS from the Debian archive
// path of the same point release and architecture.
func TestAMissingSourceImageIsFetchedFromTheDebianArchive(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		target := filepath.Join(t.TempDir(), "debian-12.10.0-"+arch+"-netinst.iso")
		cmd := exec.Command("make", "-n", "DEBIAN_"+strings.ToUpper(arch)+"_ISO="+target, target)
		cmd.Dir = repoRoot
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("make -n %s: %v\n%s", target, err, out)
		}
		want := "https://cdimage.debian.org/cdimage/archive/12.10.0/" + arch + "/iso-cd/debian-12.10.0-" + arch + "-netinst.iso"
		if !strings.Contains(string(out), want) {
			t.Errorf("%s is not fetched from %s:\n%s", arch, want, out)
		}
		if !strings.Contains(string(out), "--fail") {
			t.Errorf("the %s download does not fail on an HTTP error:\n%s", arch, out)
		}
	}
}
