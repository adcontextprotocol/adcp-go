package adcp_test

import (
	"os/exec"
	"testing"
)

// TestGeneratorDeprecatedPropagation runs the Python unit tests for
// adcp/schemas/generate.py to verify that JSON Schema deprecated:true
// properties produce a preceding // Deprecated: doc comment in the emitted
// Go struct. Run with: go test ./... from the adcp/ directory.
//
// Note: the adcp/ module currently has no dedicated CI step — this is a
// pre-existing gap for all adcp/ tests. Adding a "Test adcp module" step
// (working-directory: adcp) to .github/workflows/ci.yml is tracked as a
// follow-up (see issue #134). python3 is already a CI dependency (generate.py
// and lint.py are invoked in the test job), so exec'ing it here is safe.
func TestGeneratorDeprecatedPropagation(t *testing.T) {
	cmd := exec.Command("python3", "-m", "unittest", "discover",
		"-s", "schemas", "-p", "test_*.py", "-v")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Python generator tests failed:\n%s", out)
	}
	t.Logf("Python generator tests:\n%s", out)
}
