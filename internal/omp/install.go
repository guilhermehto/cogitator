package omp

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// InstallExtension writes the omp live-attention extension into
// <agentDir>/extensions/ so omp auto-discovers it for every session. When
// agentDir is empty it resolves via $PI_CODING_AGENT_DIR, $PI_CONFIG_DIR/agent,
// then ~/.omp/agent.
//
// cogitatorBin is baked into the file as the program the extension spawns, so
// the omp process does not need cogitator on its PATH. When empty the extension
// keeps the bare name "cogitator" (resolved via PATH at runtime).
//
// Returns the absolute path of the written extension file.
func InstallExtension(agentDir, cogitatorBin string) (string, error) {
	if agentDir == "" {
		resolved, err := resolveOmpAgentDir("")
		if err != nil || resolved == "" {
			return "", fmt.Errorf("omp: cannot resolve agent directory; set PI_CODING_AGENT_DIR")
		}
		agentDir = resolved
	}

	content := extensionTemplate
	if cogitatorBin != "" {
		content = strings.Replace(content, binDecl,
			`const COGITATOR_BIN = "`+jsStringEscape(cogitatorBin)+`";`, 1)
	}

	extDir := filepath.Join(agentDir, "extensions")
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		return "", fmt.Errorf("omp: create extensions dir: %w", err)
	}
	path := filepath.Join(extDir, ExtensionFilename)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("omp: write extension: %w", err)
	}
	return path, nil
}

// jsStringEscape escapes a value for safe interpolation inside a double-quoted
// JS/TS string literal (backslash and double-quote only; paths are single-line).
func jsStringEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}
