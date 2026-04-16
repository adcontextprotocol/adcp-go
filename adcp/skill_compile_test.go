package adcp_test

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestSkillSkeletonsCompile extracts the "Complete Skeleton" code block from each
// SKILL.md and verifies it compiles. This is the most valuable compilation check
// since the skeleton is what developers copy-paste to start building.
func TestSkillSkeletonsCompile(t *testing.T) {
	skillDir := filepath.Join("..", "skills")
	entries, err := os.ReadDir(skillDir)
	if err != nil {
		t.Skipf("skills directory not found: %v", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skillPath := filepath.Join(skillDir, entry.Name(), "SKILL.md")
		if _, err := os.Stat(skillPath); err != nil {
			continue
		}
		t.Run(entry.Name(), func(t *testing.T) {
			blocks := extractGoBlocks(t, skillPath)
			for _, block := range blocks {
				if !strings.Contains(block, "package main") {
					continue
				}
				// Found the complete skeleton
				checkBlockCompiles(t, block)
				return
			}
			t.Skip("no complete skeleton (package main) found")
		})
	}
}

// TestSkillProductDefinitionsCompile checks that product/signal/format variable
// definitions in skills compile against current SDK types.
func TestSkillProductDefinitionsCompile(t *testing.T) {
	skillDir := filepath.Join("..", "skills")
	entries, err := os.ReadDir(skillDir)
	if err != nil {
		t.Skipf("skills directory not found: %v", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skillPath := filepath.Join(skillDir, entry.Name(), "SKILL.md")
		if _, err := os.Stat(skillPath); err != nil {
			continue
		}
		t.Run(entry.Name(), func(t *testing.T) {
			blocks := extractGoBlocks(t, skillPath)
			for i, block := range blocks {
				// Only test blocks that define typed variables (products, formats, signals)
				if !isVarDefinition(block) {
					continue
				}
				t.Run(varName(block, i), func(t *testing.T) {
					checkSnippetCompiles(t, block)
				})
			}
		})
	}
}

func extractGoBlocks(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()

	var blocks []string
	var current strings.Builder
	inBlock := false

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "```go") && !inBlock {
			inBlock = true
			current.Reset()
			continue
		}
		if strings.HasPrefix(line, "```") && inBlock {
			inBlock = false
			blocks = append(blocks, current.String())
			continue
		}
		if inBlock {
			current.WriteString(line)
			current.WriteByte('\n')
		}
	}
	return blocks
}

func isVarDefinition(block string) bool {
	trimmed := strings.TrimSpace(block)
	// Must start with var, use adcp types, and not mix in AddTool calls
	return strings.HasPrefix(trimmed, "var ") &&
		strings.Contains(block, "adcp.") &&
		!strings.Contains(block, "AddTool") &&
		!strings.Contains(block, "func(")
}

func varName(block string, i int) string {
	lines := strings.SplitN(strings.TrimSpace(block), "\n", 2)
	name := strings.TrimSpace(lines[0])
	if len(name) > 50 {
		name = name[:50]
	}
	return strings.ReplaceAll(name, " ", "_")
}

func checkBlockCompiles(t *testing.T, block string) {
	t.Helper()

	// Skeletons intentionally import packages for the full implementation.
	// Convert regular imports to blank imports so unused imports don't fail.
	source := blankifyImports(block)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(source), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	writeGoMod(t, dir)

	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = dir
	if out, err := tidy.CombinedOutput(); err != nil {
		t.Fatalf("go mod tidy:\n%s", out)
	}

	build := exec.Command("go", "build", ".")
	build.Dir = dir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("skeleton does not compile:\n%s", out)
	}
}

// blankifyImports adds a block of `var _ = pkg.X` statements after the import
// block to suppress unused import errors. Skeleton code intentionally imports
// packages that are used in the full implementation but not the stub.
func blankifyImports(source string) string {
	// Map of import paths to a reference that uses the package
	refs := map[string]string{
		`"context"`:     "var _ = context.Background",
		`"crypto/rand"`: "var _ = rand.Read",
		`"encoding/hex"`:  "var _ = hex.EncodeToString",
		`"fmt"`:         "var _ = fmt.Sprintf",
		`"log"`:         "var _ = log.Fatal",
		`"strings"`:     "var _ = strings.Contains",
		`"sync"`:        "var _ = sync.Mutex{}",
		`"time"`:        "var _ = time.Now",
	}

	// Find which packages are imported
	var suppressions []string
	for pkg, ref := range refs {
		if strings.Contains(source, pkg) {
			suppressions = append(suppressions, ref)
		}
	}
	if len(suppressions) == 0 {
		return source
	}

	// Insert after "func main() {"
	suppBlock := "\n// Suppress unused imports for skeleton code\n" + strings.Join(suppressions, "\n") + "\n"

	// Insert right after the import block closing paren
	idx := strings.Index(source, ")\n")
	if idx > 0 {
		return source[:idx+2] + suppBlock + source[idx+2:]
	}
	return source
}

func checkSnippetCompiles(t *testing.T, block string) {
	t.Helper()

	// Wrap var definition in a package with imports
	source := `package main

import (
	"github.com/adcontextprotocol/adcp-go/adcp"
)

const agentURL = "http://localhost:3001/mcp"

` + block + `

func main() {}
var _ = adcp.EmptyInput{}
`

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(source), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	writeGoMod(t, dir)

	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = dir
	if out, err := tidy.CombinedOutput(); err != nil {
		t.Fatalf("go mod tidy:\n%s", out)
	}

	build := exec.Command("go", "build", ".")
	build.Dir = dir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("snippet does not compile:\n%s\n\n--- source ---\n%s", out, source)
	}
}

func writeGoMod(t *testing.T, dir string) {
	t.Helper()
	adcpDir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	goMod := `module skill_check

go 1.26.2

require (
	github.com/adcontextprotocol/adcp-go/adcp v0.0.0
	github.com/modelcontextprotocol/go-sdk v1.5.0
)

replace github.com/adcontextprotocol/adcp-go/adcp => ` + adcpDir + "\n"

	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
}
