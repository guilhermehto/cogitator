package omp_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guilhermehto/cogitator/internal/omp"
)

func TestInstallExtension_BakesBinaryPath(t *testing.T) {
	agentDir := t.TempDir()
	bin := "/opt/tools/cogitator"

	path, err := omp.InstallExtension(agentDir, bin)
	if err != nil {
		t.Fatalf("InstallExtension: %v", err)
	}
	want := filepath.Join(agentDir, "extensions", "cogitator.ts")
	if path != want {
		t.Errorf("path = %q, want %q", path, want)
	}

	content := readFile(t, path)
	if !strings.Contains(content, `const COGITATOR_BIN = "/opt/tools/cogitator";`) {
		t.Errorf("installed extension did not bake in the binary path:\n%s", content)
	}
	if strings.Contains(content, `const COGITATOR_BIN = "cogitator";`) {
		t.Error("installed extension still has the bare-name default declaration")
	}
}

func TestInstallExtension_EmptyBinKeepsBareName(t *testing.T) {
	agentDir := t.TempDir()
	path, err := omp.InstallExtension(agentDir, "")
	if err != nil {
		t.Fatalf("InstallExtension: %v", err)
	}
	content := readFile(t, path)
	if !strings.Contains(content, `const COGITATOR_BIN = "cogitator";`) {
		t.Errorf("empty bin should keep bare name; got:\n%s", content)
	}
}

func TestInstallExtension_EscapesPath(t *testing.T) {
	agentDir := t.TempDir()
	// A path containing a quote and backslash must not break the TS literal.
	bin := `C:\tools\cog "x".exe`
	path, err := omp.InstallExtension(agentDir, bin)
	if err != nil {
		t.Fatalf("InstallExtension: %v", err)
	}
	content := readFile(t, path)
	if !strings.Contains(content, `const COGITATOR_BIN = "C:\\tools\\cog \"x\".exe";`) {
		t.Errorf("path was not JS-escaped into the literal:\n%s", content)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	return string(b)
}
