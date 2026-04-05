package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestDockerBuild verifies that all service Dockerfiles build successfully
// from the project root context. This catches issues like:
// - missing go.sum files
// - wrong replace directive paths for the shared module
// - missing shared/ copy in Dockerfile
// - COPY . . copying conflicting go.mod files from other services
func TestDockerBuild(t *testing.T) {
	root := findProjectRoot()
	if root == "" {
		t.Fatal("could not determine project root")
	}

	services := []struct {
		name      string
		dockerfile string
	}{
		{"agent", "services/agent/Dockerfile"},
		{"fake-service", "services/fake-service/Dockerfile"},
		{"ingestion", "services/ingestion/Dockerfile"},
		{"analyzer", "services/analyzer/Dockerfile"},
		{"notifier", "services/notifier/Dockerfile"},
	}

	for _, svc := range services {
		t.Run(svc.name, func(t *testing.T) {
			cmd := exec.Command("docker", "build",
				"-f", svc.dockerfile,
				"--quiet",
				root,
			)
			cmd.Dir = root
			cmd.Env = append(cmd.Env, "DOCKER_BUILDKIT=0")
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("docker build -f %s failed (root=%q):\n%s\nerror: %v", svc.dockerfile, root, out, err)
			}
		})
	}
}

// findProjectRoot locates the project root by searching for go.work upward.
func findProjectRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(wd, "go.work")); err == nil {
			return wd
		}
		parent := filepath.Dir(wd)
		if parent == wd {
			return ""
		}
		wd = parent
	}
}
