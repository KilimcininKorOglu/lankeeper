package buildsys

import (
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// volumeFlag matches every short and long spelling of a volume mount:
// "-v SRC:DST[:OPTS]", "--volume SRC:DST[:OPTS]" and "--volume=...".
var volumeFlag = regexp.MustCompile(`(?:^|\s)(?:-v|--volume)(?:\s+|=)(\S+)`)

// mountFlag matches "--mount KEY=VAL,..." and "--mount=KEY=VAL,...".
var mountFlag = regexp.MustCompile(`(?:^|\s)--mount(?:\s+|=)(\S+)`)

type bindMount struct {
	source, dest string
	readOnly     bool
}

// parseBindMounts returns every bind mount in a docker run command line,
// whichever syntax declares it.
func parseBindMounts(recipe string) []bindMount {
	var mounts []bindMount
	for _, m := range volumeFlag.FindAllStringSubmatch(recipe, -1) {
		parts := strings.Split(m[1], ":")
		if len(parts) < 2 {
			continue
		}
		mount := bindMount{source: parts[0], dest: parts[1]}
		if len(parts) > 2 {
			mount.readOnly = slices.Contains(strings.Split(parts[2], ","), "ro")
		}
		mounts = append(mounts, mount)
	}
	for _, m := range mountFlag.FindAllStringSubmatch(recipe, -1) {
		mounts = append(mounts, parseMountSpec(m[1]))
	}
	return mounts
}

// parseMountSpec reads the comma-separated key=value form of --mount.
func parseMountSpec(spec string) bindMount {
	var mount bindMount
	for field := range strings.SplitSeq(spec, ",") {
		key, value, _ := strings.Cut(field, "=")
		switch key {
		case "src", "source":
			mount.source = value
		case "dst", "destination", "target":
			mount.dest = value
		case "ro", "readonly":
			mount.readOnly = value == "" || value == "true" || value == "1"
		}
	}
	return mount
}

// mountProblems lists every mount that exposes the source tree: the
// repository root anywhere, and any writable mount other than dist/.
func mountProblems(recipe, repoRoot string) (problems []string, sawDist bool) {
	for _, m := range parseBindMounts(recipe) {
		switch {
		case m.source == repoRoot || strings.TrimSuffix(m.source, "/") == repoRoot || m.dest == "/build":
			problems = append(problems, "mounts the repository root at "+m.dest)
		case m.source == repoRoot+"/dist" && m.dest == "/build/dist":
			sawDist = true
		case !m.readOnly:
			problems = append(problems, "mounts "+m.source+" writable at "+m.dest+"; only dist/ needs to be written")
		}
	}
	return problems, sawDist
}

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

			problems, sawDist := mountProblems(recipe, repoRoot)
			for _, p := range problems {
				t.Errorf("%s %s", target, p)
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

// TestMountProblemsSeesEverySyntax pins the parser the guard above rests
// on: a writable repository mount written with --volume or --mount, a
// writable mount at /build/dist from another source, and an option that
// merely contains the letters "ro" must all be reported.
func TestMountProblemsSeesEverySyntax(t *testing.T) {
	const root = "/repo"
	bad := []string{
		"docker run --volume /repo:/build/src img",
		"docker run --volume=/repo:/src img",
		"docker run --mount type=bind,src=/repo,dst=/src img",
		"docker run --mount=type=bind,source=/repo/internal,target=/x img",
		"docker run -v /repo/internal:/build/dist img",
		"docker run -v /repo/deploy:/build/deploy:rprivate img",
	}
	for _, recipe := range bad {
		if problems, _ := mountProblems(recipe, root); len(problems) == 0 {
			t.Errorf("no problem reported for %q", recipe)
		}
	}

	good := "docker run -v /repo/configs:/build/configs:ro --mount type=bind,src=/repo/deploy,dst=/build/deploy,readonly -v /repo/dist:/build/dist img"
	problems, sawDist := mountProblems(good, root)
	if len(problems) != 0 || !sawDist {
		t.Errorf("safe recipe: problems %v, sawDist %v", problems, sawDist)
	}
}
