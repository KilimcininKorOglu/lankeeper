package buildsys

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// bindMount captures one -v argument: source, destination, and the
// options that follow.
var bindMount = regexp.MustCompile(`-v\s+(\S+?):(/[^:\s]+)(:[a-z,]+)?`)

// expandISOTarget asks make to print the recipe without running it, so
// the assertions below see the same command line Docker would.
func expandISOTarget(t *testing.T, target string) string {
	t.Helper()

	cmd := exec.Command("make", "-n", target)
	cmd.Dir = ".."
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make -n %s: %v\n%s", target, err, out)
	}

	for line := range strings.SplitSeq(string(out), "\n") {
		if strings.Contains(line, "docker run") || strings.Contains(line, "-v ") {
			// The recipe is one logical line continued across several
			// physical ones, so return the whole output and let the
			// caller match against it.
			return string(out)
		}
	}
	t.Fatalf("no docker invocation in `make -n %s`:\n%s", target, out)
	return ""
}

// TestTheISOBuilderGetsNoWritableSource is the regression test. Both
// targets mounted the repository root writable, and the builder image
// declares no USER, so its entrypoint runs as root over internal/,
// cmd/, web/ and .git for no functional reason. The script reads
// configs/ and deploy/ and writes only under dist/.
//
// The exposure matters as an amplifier rather than on its own: a
// container compromised through another vector could edit Go source or
// git history so the maintainer's next host-side build compiles the
// change faithfully, turning a one-time break into a persistent one.
func TestTheISOBuilderGetsNoWritableSource(t *testing.T) {
	repoRoot, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	repoRoot = strings.TrimSuffix(repoRoot, "/buildsys")

	for _, target := range []string{"iso-amd64", "iso-arm64"} {
		t.Run(target, func(t *testing.T) {
			recipe := expandISOTarget(t, target)

			var sawDist bool
			for _, m := range bindMount.FindAllStringSubmatch(recipe, -1) {
				source, dest, opts := m[1], m[2], m[3]
				readOnly := strings.Contains(opts, "ro")

				if source == repoRoot || dest == "/build" {
					t.Errorf("%s mounts the repository root at %s", target, dest)
					continue
				}
				if dest == "/build/dist" {
					sawDist = true
					continue
				}
				if !readOnly {
					t.Errorf("%s mounts %s writable; only dist/ needs to be written",
						target, dest)
				}
			}

			if !sawDist {
				t.Errorf("%s never mounts dist/, so the built image has nowhere to land", target)
			}
		})
	}
}

// TestTheISOBuilderStillGetsWhatItReads keeps the narrowing from
// breaking the build it protects. The script resolves its project root
// from its own path, so every one of these has to be present at the
// same layout under /build.
func TestTheISOBuilderStillGetsWhatItReads(t *testing.T) {
	for _, target := range []string{"iso-amd64", "iso-arm64"} {
		recipe := expandISOTarget(t, target)

		for _, dest := range []string{"/build/configs", "/build/deploy", "/build/dist"} {
			if !strings.Contains(recipe, dest) {
				t.Errorf("%s does not mount %s", target, dest)
			}
		}
		if !strings.Contains(recipe, "/debian.iso:ro") {
			t.Errorf("%s does not mount the source image read-only", target)
		}
	}
}

// TestTheISOBuilderTakesNoHostPrivileges pins the rest of the
// invocation, which is already correct: the build touches files and
// byte offsets, so it needs no capability beyond its own mounts.
func TestTheISOBuilderTakesNoHostPrivileges(t *testing.T) {
	forbidden := []string{
		"--privileged",
		"--cap-add",
		"--net=host",
		"--network=host",
		"--pid=host",
		"docker.sock",
	}

	for _, target := range []string{"iso-amd64", "iso-arm64"} {
		recipe := expandISOTarget(t, target)
		for _, f := range forbidden {
			if strings.Contains(recipe, f) {
				t.Errorf("%s passes %s to the builder", target, f)
			}
		}
	}
}
