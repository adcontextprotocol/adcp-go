package adcp_test

import (
	"os/exec"
	"testing"
)

// TestGeneratorPython runs all schema generator Python unit tests. Uses the
// same *_test.py discovery pattern as the CI "Test schema generator" step so
// the two paths stay in sync. Run with: go test ./... from the adcp/ directory.
func TestGeneratorPython(t *testing.T) {
	cmd := exec.Command("python3", "-m", "unittest", "discover",
		"-s", "schemas", "-p", "*_test.py", "-v")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Python generator tests failed:\n%s", out)
	}
	t.Logf("Python generator tests:\n%s", out)
}
