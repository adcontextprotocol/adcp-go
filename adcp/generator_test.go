package adcp_test

import (
	"os/exec"
	"testing"
)

// TestGeneratorDeprecatedPropagation runs the Python unit tests for
// adcp/schemas/generate.py to verify that JSON Schema deprecated:true
// properties produce a preceding // Deprecated: doc comment in the emitted
// Go struct. This bridges the Python test suite into go test ./... so CI
// coverage does not require a separate workflow step.
//
// python3 is a build-time dependency of this module (generate.py and lint.py
// are already invoked by the CI test job), so exec'ing it here is safe.
func TestGeneratorDeprecatedPropagation(t *testing.T) {
	cmd := exec.Command("python3", "-m", "unittest", "discover",
		"-s", "schemas", "-p", "test_*.py", "-v")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Python generator tests failed:\n%s", out)
	}
	t.Logf("Python generator tests:\n%s", out)
}
