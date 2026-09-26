package buildsys

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// releaseWorkflowPath is the workflow that builds, signs and publishes
// a release.
var releaseWorkflowPath = filepath.Join("..", ".github", "workflows", "release.yml")

type releaseWorkflow struct {
	On          map[string]map[string]any `yaml:"on"`
	Permissions map[string]string         `yaml:"permissions"`
	Jobs        map[string]releaseJob     `yaml:"jobs"`
}

type releaseJob struct {
	If          string            `yaml:"if"`
	Environment string            `yaml:"environment"`
	Permissions map[string]string `yaml:"permissions"`
	Strategy    struct {
		Matrix struct {
			Include []map[string]string `yaml:"include"`
		} `yaml:"matrix"`
	} `yaml:"strategy"`
	Steps []struct {
		Run string `yaml:"run"`
	} `yaml:"steps"`
}

// loadReleaseWorkflow returns the parsed workflow and each job's own
// text, re-marshalled, for checks that scan everything a job carries.
func loadReleaseWorkflow(t *testing.T) (releaseWorkflow, map[string]string) {
	t.Helper()
	raw, err := os.ReadFile(releaseWorkflowPath)
	if err != nil {
		t.Fatalf("read %s: %v", releaseWorkflowPath, err)
	}
	var wf releaseWorkflow
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		t.Fatalf("parse %s: %v", releaseWorkflowPath, err)
	}
	var generic struct {
		Jobs map[string]any `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("parse %s: %v", releaseWorkflowPath, err)
	}
	texts := map[string]string{}
	for name, job := range generic.Jobs {
		out, err := yaml.Marshal(job)
		if err != nil {
			t.Fatalf("marshal job %s: %v", name, err)
		}
		texts[name] = string(out)
	}
	return wf, texts
}

// The signing key is read by a workflow, so what can start that workflow
// decides who can reach the key. A pull_request trigger would run it for
// code nobody reviewed, and a branch push for commits nobody tagged.
func TestReleaseRunsOnTagsAndManualDispatchOnly(t *testing.T) {
	wf, _ := loadReleaseWorkflow(t)

	var triggers []string
	for name := range wf.On {
		triggers = append(triggers, name)
	}
	slices.Sort(triggers)
	if !slices.Equal(triggers, []string{"push", "workflow_dispatch"}) {
		t.Fatalf("triggers = %v, want push and workflow_dispatch", triggers)
	}
	push := wf.On["push"]
	if len(push) != 1 {
		t.Errorf("push filters = %v, want tags only", push)
	}
	tags, _ := push["tags"].([]any)
	if len(tags) != 1 || tags[0] != "v*" {
		t.Errorf("push tags = %v, want [v*]", push["tags"])
	}
}

// Only the job that runs in the approved `release` environment, and only
// on a tag push, may read a secret or write to the repository. The build
// jobs run arbitrary build tooling and hold neither.
func TestReleaseSecretsStayInTheApprovedJob(t *testing.T) {
	wf, texts := loadReleaseWorkflow(t)

	if len(wf.Permissions) != 1 || wf.Permissions["contents"] != "read" {
		t.Errorf("workflow permissions = %v, want contents: read only", wf.Permissions)
	}
	signers := 0
	for name, job := range wf.Jobs {
		if !jobIsPrivileged(job, texts[name]) {
			continue
		}
		if job.Environment != "release" || !strings.Contains(job.If, "github.event_name == 'push'") {
			t.Errorf("job %s reads a secret or writes, outside the release environment on a tag push", name)
		}
		if strings.Contains(texts[name], "secrets.RELEASE_SIGNING_KEY") {
			signers++
		}
	}
	if signers != 1 {
		t.Errorf("%d jobs read the signing key, want exactly one", signers)
	}
}

// jobIsPrivileged reports whether a job references a secret or holds a
// write permission.
func jobIsPrivileged(job releaseJob, text string) bool {
	if strings.Contains(text, "secrets.") {
		return true
	}
	for _, level := range job.Permissions {
		if level == "write" {
			return true
		}
	}
	return false
}

// An expression expanded inside a run script is pasted into the shell
// before it runs, so a crafted ref name would execute in the job that
// holds the signing key. Values reach scripts through env instead.
func TestReleaseRunScriptsInterpolateNoExpressions(t *testing.T) {
	wf, _ := loadReleaseWorkflow(t)
	for name, job := range wf.Jobs {
		for i, step := range job.Steps {
			if strings.Contains(step.Run, "${{") {
				t.Errorf("job %s step %d expands an expression inside its script", name, i+1)
			}
		}
	}
}

// Both shipped architectures are built, arm64 on a native runner rather
// than under emulation.
func TestReleaseBuildsBothArchitectures(t *testing.T) {
	wf, _ := loadReleaseWorkflow(t)
	build, ok := wf.Jobs["build"]
	if !ok {
		t.Fatal("release.yml has no build job")
	}
	runners := map[string]string{}
	for _, leg := range build.Strategy.Matrix.Include {
		runners[leg["arch"]] = leg["runner"]
	}
	if len(runners) != 2 || runners["amd64"] == "" {
		t.Errorf("build matrix = %v, want amd64 and arm64", runners)
	}
	if !strings.HasSuffix(runners["arm64"], "-arm") {
		t.Errorf("arm64 builds on %q, want a native arm64 runner", runners["arm64"])
	}
}
