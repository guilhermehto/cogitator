package omp

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/guilhermehto/cogitator/internal/installkit"
)

// extensionTemplate is the TypeScript live-attention extension installed into
// the omp extensions directory. The binDecl line below is rewritten with the
// cogitator binary path at install time.
//
//go:embed cogitator.ts
var extensionTemplate string

// ExtensionFilename is the name of the installed omp extension file. It matches
// the name omp derives the extension id from ("cogitator").
const ExtensionFilename = "cogitator.ts"

// binDecl is the exact source line the extension uses to declare the cogitator
// binary. InstallExtension swaps the bare-name default for an absolute path so
// the installed copy does not depend on the omp process PATH.
const binDecl = `const COGITATOR_BIN = "cogitator";`

// binDeclPattern matches any baked COGITATOR_BIN declaration line, capturing
// the JS-escaped binary.
var binDeclPattern = regexp.MustCompile(`(?m)^const COGITATOR_BIN = "((?:[^"\\]|\\.)*)";$`)

// stampPrefix opens the header line that marks a file cogitator wrote.
const stampPrefix = "// managed by cogitator (sha256:"

// InstallExtension writes the omp live-attention extension into
// <agentDir>/extensions/ so omp auto-discovers it for every session. When
// agentDir is empty it resolves via $PI_CODING_AGENT_DIR, $PI_CONFIG_DIR/agent,
// then ~/.omp/agent.
//
// cogitatorBin is baked into the file as the program the extension spawns, so
// the omp process does not need cogitator on its PATH. When empty the extension
// keeps the bare name "cogitator" (resolved via PATH at runtime).
//
// Returns the status found before installing; its Path is the extension file
// except for Foreign, where it names the file that wired cogitator. A Foreign
// status is refused with *installkit.ForeignError unless force is set; a
// Current status writes nothing.
func InstallExtension(agentDir, cogitatorBin string, force bool) (installkit.Status, error) {
	agentDir, err := extensionAgentDir(agentDir)
	if err != nil {
		return installkit.Status{}, err
	}

	status, err := CheckExtension(agentDir, cogitatorBin)
	if err != nil {
		return installkit.Status{}, err
	}

	if status.State == installkit.Foreign && !force {
		return status, &installkit.ForeignError{Status: status}
	}

	if status.State == installkit.Current {
		return status, nil
	}

	path := filepath.Join(agentDir, "extensions", ExtensionFilename)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return status, fmt.Errorf("omp: create extensions dir: %w", err)
	}

	if err := os.WriteFile(path, []byte(renderExtension(cogitatorBin)), 0o600); err != nil {
		return status, fmt.Errorf("omp: write extension: %w", err)
	}

	return status, nil
}

// CheckExtension reports how the cogitator wiring in <agentDir>/extensions
// compares with what InstallExtension would write for cogitatorBin. It never
// writes. agentDir "" resolves like InstallExtension.
func CheckExtension(agentDir, cogitatorBin string) (installkit.Status, error) {
	agentDir, err := extensionAgentDir(agentDir)
	if err != nil {
		return installkit.Status{}, err
	}

	extDir := filepath.Join(agentDir, "extensions")
	foreign, err := findForeignExtension(extDir)
	if err != nil {
		return installkit.Status{}, err
	}

	if foreign != nil {
		return *foreign, nil
	}

	path := filepath.Join(extDir, ExtensionFilename)
	existing, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return installkit.Status{State: installkit.Missing, Path: path}, nil
	}

	if err != nil {
		return installkit.Status{}, fmt.Errorf("omp: read extension: %w", err)
	}

	return classifyExtension(path, string(existing), cogitatorBin), nil
}

func extensionAgentDir(agentDir string) (string, error) {
	if agentDir != "" {
		return agentDir, nil
	}

	resolved, err := resolveOmpAgentDir("")
	if err != nil || resolved == "" {
		return "", fmt.Errorf("omp: cannot resolve agent directory; set PI_CODING_AGENT_DIR")
	}

	return resolved, nil
}

func classifyExtension(path, existing, cogitatorBin string) installkit.Status {
	status := installkit.Status{Path: path}
	switch {
	case existing == renderExtension(cogitatorBin):
		status.State = installkit.Current
	case hasValidStamp(existing), isLegacyExtension(existing):
		status.State = installkit.Stale
		status.Detail = staleDetail(existing, cogitatorBin)
	default:
		status.State = installkit.Foreign
		status.Detail = "hand-edited extension"
	}

	return status
}

// staleDetail names the binary change, or the template change when the
// binary is the same.
func staleDetail(existing, cogitatorBin string) string {
	want := cogitatorBin
	if want == "" {
		want = "cogitator"
	}

	match := binDeclPattern.FindStringSubmatch(existing)
	if match == nil {
		return "extension template changed"
	}

	old, err := strconv.Unquote(`"` + match[1] + `"`)
	if err != nil || old == want {
		return "extension template changed"
	}

	return old + " -> " + want
}

// findForeignExtension returns a Foreign status for the first regular file in
// extDir, other than cogitator's own, that spawns omp-hook.
func findForeignExtension(extDir string) (*installkit.Status, error) {
	entries, err := os.ReadDir(extDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("omp: read extensions dir: %w", err)
	}

	for _, entry := range entries {
		if entry.Name() == ExtensionFilename || !entry.Type().IsRegular() {
			continue
		}

		path := filepath.Join(extDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("omp: read extension %s: %w", path, err)
		}

		for i, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, "omp-hook") {
				return &installkit.Status{
					State:  installkit.Foreign,
					Path:   path,
					Line:   i + 1,
					Detail: strings.TrimSpace(line),
				}, nil
			}
		}
	}

	return nil, nil
}

// renderExtension is the exact file InstallExtension writes: a stamp header
// carrying the sha256 of the body, then the template with cogitatorBin baked in.
func renderExtension(cogitatorBin string) string {
	body := extensionTemplate
	if cogitatorBin != "" {
		body = strings.Replace(body, binDecl,
			`const COGITATOR_BIN = "`+jsStringEscape(cogitatorBin)+`";`, 1)
	}

	return stampLine(body) + body
}

func stampLine(body string) string {
	sum := sha256.Sum256([]byte(body))

	return stampPrefix + hex.EncodeToString(sum[:]) + ") — regenerate with `cogitator omp-hook install`\n"
}

// hasValidStamp reports whether content is an unedited file cogitator wrote:
// its header stamp matches the sha256 of the body below it.
func hasValidStamp(content string) bool {
	if !strings.HasPrefix(content, stampPrefix) {
		return false
	}

	_, body, ok := strings.Cut(content, "\n")

	return ok && content[:len(content)-len(body)] == stampLine(body)
}

// isLegacyExtension reports whether content is a stamp-less extension written
// before stamps existed, differing from the template only in its binary line.
func isLegacyExtension(content string) bool {
	if strings.HasPrefix(content, stampPrefix) {
		return false
	}

	return binDeclPattern.ReplaceAllLiteralString(content, binDecl) == extensionTemplate
}

// jsStringEscape escapes a value for safe interpolation inside a double-quoted
// JS/TS string literal (backslash and double-quote only; paths are single-line).
func jsStringEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}
