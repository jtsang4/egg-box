package ci_test

import (
	"os"
	"path/filepath"
	"testing"

	"go.yaml.in/yaml/v3"
)

type job struct {
	Needs any            `yaml:"needs"`
	Uses  string         `yaml:"uses"`
	If    string         `yaml:"if"`
	With  map[string]any `yaml:"with"`
}

type workflow struct {
	On   map[string]yaml.Node `yaml:"on"`
	Jobs map[string]job       `yaml:"jobs"`
}

func load(t *testing.T, name string) workflow {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", name))
	if err != nil {
		t.Fatal(err)
	}
	var w workflow
	if err := yaml.Unmarshal(b, &w); err != nil {
		t.Fatal(err)
	}
	return w
}

func depends(j job, target string) bool {
	switch n := j.Needs.(type) {
	case string:
		return n == target
	case []any:
		for _, dependency := range n {
			if dependency == target {
				return true
			}
		}
	}
	return false
}

func TestCIRequiresExplicitInvocation(t *testing.T) {
	w := load(t, "ci.yml")
	for _, event := range []string{"push", "pull_request", "pull_request_target"} {
		if _, exists := w.On[event]; exists {
			t.Errorf("CI must not run automatically on %s", event)
		}
	}
	for _, event := range []string{"workflow_dispatch", "workflow_call"} {
		if _, exists := w.On[event]; !exists {
			t.Errorf("CI must support %s", event)
		}
	}
}

func TestReleaseCannotBuildOrPublishBeforeVerification(t *testing.T) {
	w := load(t, "release.yml")
	gate := ""
	for id, j := range w.Jobs {
		if j.Uses == "./.github/workflows/ci.yml" {
			gate = id
			if value, exists := j.With["installers"]; !exists || value != false {
				t.Fatal("release verification must avoid duplicate unsigned installers")
			}
			if !depends(j, "version") {
				t.Fatal("the release version must be checked before verification")
			}
		}
	}
	if gate == "" {
		t.Fatal("release must invoke the complete reusable CI workflow")
	}
	for _, id := range []string{"macos", "windows", "publish"} {
		j, exists := w.Jobs[id]
		if !exists || !depends(j, gate) || j.If != "" {
			t.Errorf("%s must wait for successful verification", id)
		}
	}
}

func TestVerificationIncludesNativeAndGUIJobsWithoutInstallers(t *testing.T) {
	w := load(t, "ci.yml")
	if w.Jobs["check"].If != "github.event_name != 'schedule'" {
		t.Fatal("the full check tier must run for manual and release invocations")
	}
	for _, id := range []string{"gui", "macos", "windows", "cross"} {
		j, exists := w.Jobs[id]
		if !exists || !depends(j, "check") || j.If != "" {
			t.Errorf("%s must run after checks regardless of the installers option", id)
		}
	}
}
