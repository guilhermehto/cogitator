package omp_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guilhermehto/cogitator/internal/installkit"
	"github.com/guilhermehto/cogitator/internal/omp"
)

func TestInstallExtension_BakesBinaryPath(t *testing.T) {
	agentDir := t.TempDir()
	bin := "/opt/tools/cogitator"

	status, err := omp.InstallExtension(agentDir, bin, false)
	if err != nil {
		t.Fatalf("InstallExtension: %v", err)
	}
	path := filepath.Join(agentDir, "extensions", "cogitator.ts")
	if status.Path != path || status.State != installkit.Missing {
		t.Errorf("status = %+v, want missing at %q", status, path)
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
	status, err := omp.InstallExtension(agentDir, "", false)
	if err != nil {
		t.Fatalf("InstallExtension: %v", err)
	}
	content := readFile(t, status.Path)
	if !strings.Contains(content, `const COGITATOR_BIN = "cogitator";`) {
		t.Errorf("empty bin should keep bare name; got:\n%s", content)
	}
}

func TestInstallExtension_EscapesPath(t *testing.T) {
	agentDir := t.TempDir()
	// A path containing a quote and backslash must not break the TS literal.
	bin := `C:\tools\cog "x".exe`
	status, err := omp.InstallExtension(agentDir, bin, false)
	if err != nil {
		t.Fatalf("InstallExtension: %v", err)
	}
	content := readFile(t, status.Path)
	if !strings.Contains(content, `const COGITATOR_BIN = "C:\\tools\\cog \"x\".exe";`) {
		t.Errorf("path was not JS-escaped into the literal:\n%s", content)
	}
}

func TestInstallExtension_StampsManagedHeader(t *testing.T) {
	status, err := omp.InstallExtension(t.TempDir(), "/opt/tools/cogitator", false)
	if err != nil {
		t.Fatalf("InstallExtension: %v", err)
	}
	first, _, _ := strings.Cut(readFile(t, status.Path), "\n")
	if !strings.HasPrefix(first, "// managed by cogitator (sha256:") || !strings.HasSuffix(first, "`cogitator omp-hook install`") {
		t.Errorf("header line = %q, want managed-by stamp", first)
	}
}

func TestCheckExtension_States(t *testing.T) {
	const bin = "/opt/tools/cogitator"
	tests := []struct {
		name       string
		setup      func(t *testing.T, agentDir string)
		wantState  installkit.State
		wantFile   string
		wantLine   int
		wantDetail string
	}{
		{
			name:      "no extension is missing",
			setup:     func(*testing.T, string) {},
			wantState: installkit.Missing,
			wantFile:  "cogitator.ts",
		},
		{
			name: "extension installed for this binary is current",
			setup: func(t *testing.T, agentDir string) {
				install(t, agentDir, bin)
			},
			wantState: installkit.Current,
			wantFile:  "cogitator.ts",
		},
		{
			name: "extension installed for another binary is stale",
			setup: func(t *testing.T, agentDir string) {
				install(t, agentDir, "/old/cogitator")
			},
			wantState: installkit.Stale,
			wantFile:  "cogitator.ts",
		},
		{
			name: "legacy unstamped extension differing only in binary is stale",
			setup: func(t *testing.T, agentDir string) {
				path := install(t, agentDir, "/old/cogitator")
				_, body, _ := strings.Cut(readFile(t, path), "\n")
				writeFile(t, path, body)
			},
			wantState: installkit.Stale,
			wantFile:  "cogitator.ts",
		},
		{
			name: "hand-edited extension is foreign",
			setup: func(t *testing.T, agentDir string) {
				path := install(t, agentDir, bin)
				writeFile(t, path, readFile(t, path)+"// my tweak\n")
			},
			wantState:  installkit.Foreign,
			wantFile:   "cogitator.ts",
			wantDetail: "hand-edited extension",
		},
		{
			name: "hand-edited legacy extension is foreign",
			setup: func(t *testing.T, agentDir string) {
				path := install(t, agentDir, bin)
				_, body, _ := strings.Cut(readFile(t, path), "\n")
				writeFile(t, path, strings.Replace(body, "omp-hook", "omp-hook --verbose", 1))
			},
			wantState:  installkit.Foreign,
			wantFile:   "cogitator.ts",
			wantDetail: "hand-edited extension",
		},
		{
			name: "other extension spawning omp-hook is foreign",
			setup: func(t *testing.T, agentDir string) {
				install(t, agentDir, bin)
				writeFile(t, filepath.Join(agentDir, "extensions", "mine.ts"),
					"// mine\n  Bun.spawn([\"cogitator\", \"omp-hook\"]);\n")
			},
			wantState:  installkit.Foreign,
			wantFile:   "mine.ts",
			wantLine:   2,
			wantDetail: `Bun.spawn(["cogitator", "omp-hook"]);`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agentDir := t.TempDir()
			tt.setup(t, agentDir)

			status, err := omp.CheckExtension(agentDir, bin)
			if err != nil {
				t.Fatalf("CheckExtension: %v", err)
			}
			want := installkit.Status{
				State:  tt.wantState,
				Path:   filepath.Join(agentDir, "extensions", tt.wantFile),
				Line:   tt.wantLine,
				Detail: tt.wantDetail,
			}
			if status != want {
				t.Errorf("status = %+v, want %+v", status, want)
			}
		})
	}
}

func TestInstallExtension_RewritesStaleFileInPlace(t *testing.T) {
	agentDir := t.TempDir()
	install(t, agentDir, "/old/cogitator")

	status, err := omp.InstallExtension(agentDir, "/new/cogitator", false)
	if err != nil {
		t.Fatalf("InstallExtension: %v", err)
	}
	if status.State != installkit.Stale {
		t.Errorf("state = %v, want stale", status.State)
	}

	entries, err := os.ReadDir(filepath.Join(agentDir, "extensions"))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "cogitator.ts" {
		t.Errorf("extensions dir = %v, want only cogitator.ts", entries)
	}
	content := readFile(t, status.Path)
	if !strings.Contains(content, `const COGITATOR_BIN = "/new/cogitator";`) || strings.Contains(content, "/old/cogitator") {
		t.Errorf("stale extension was not rewritten with the new binary:\n%s", content)
	}
}

func TestInstallExtension_HandEditedFileNeedsForce(t *testing.T) {
	agentDir := t.TempDir()
	path := install(t, agentDir, "/opt/tools/cogitator")
	edited := readFile(t, path) + "// my tweak\n"
	writeFile(t, path, edited)

	_, err := omp.InstallExtension(agentDir, "/opt/tools/cogitator", false)
	var foreign *installkit.ForeignError
	if !errors.As(err, &foreign) {
		t.Fatalf("err = %v, want *installkit.ForeignError", err)
	}
	if got := readFile(t, path); got != edited {
		t.Errorf("hand-edited extension changed without force:\n%s", got)
	}

	status, err := omp.InstallExtension(agentDir, "/opt/tools/cogitator", true)
	if err != nil {
		t.Fatalf("forced InstallExtension: %v", err)
	}
	if status.State != installkit.Foreign {
		t.Errorf("state = %v, want foreign", status.State)
	}
	if got := readFile(t, path); strings.Contains(got, "my tweak") {
		t.Errorf("forced install kept the hand edit:\n%s", got)
	}
	check, err := omp.CheckExtension(agentDir, "/opt/tools/cogitator")
	if err != nil || check.State != installkit.Current {
		t.Errorf("after forced install: status = %+v, err = %v; want current", check, err)
	}
}

func TestInstallExtension_TwiceIsIdempotent(t *testing.T) {
	agentDir := t.TempDir()
	path := install(t, agentDir, "/opt/tools/cogitator")
	first := readFile(t, path)

	status, err := omp.InstallExtension(agentDir, "/opt/tools/cogitator", false)
	if err != nil {
		t.Fatalf("second InstallExtension: %v", err)
	}
	if status.State != installkit.Current {
		t.Errorf("state = %v, want current", status.State)
	}
	if got := readFile(t, path); got != first {
		t.Error("second install changed the extension file")
	}
}

func install(t *testing.T, agentDir, bin string) string {
	t.Helper()
	status, err := omp.InstallExtension(agentDir, bin, false)
	if err != nil {
		t.Fatalf("InstallExtension: %v", err)
	}
	return status.Path
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
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
