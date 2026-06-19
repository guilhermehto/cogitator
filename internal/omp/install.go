package omp

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// extensionTemplate is the JS hook bridge installed into the omp extensions
// directory. The "__COGITATOR_BIN__" placeholder is substituted with the
// cogitator binary path at install time.
//
//go:embed extension.js
var extensionTemplate string

// ExtensionFilename is the name of the installed omp hook bridge extension.
const ExtensionFilename = "cogitator-omp.js"

// InstallExtension writes the omp hook bridge extension into
// <agentDir>/extensions/ so omp auto-discovers it for every session. When
// agentDir is empty it resolves $PI_CODING_AGENT_DIR, then ~/.omp/agent.
//
// cogitatorBin is baked into the file as the program the bridge spawns, so the
// hook runner does not depend on inheriting an interactive shell PATH; when
// empty it defaults to "cogitator" (resolved via PATH at runtime).
//
// Returns the absolute path of the written extension file.
func InstallExtension(agentDir, cogitatorBin string) (string, error) {
	if agentDir == "" {
		resolved, err := resolveOMPHome("")
		if err != nil || resolved == "" {
			return "", fmt.Errorf("omp: cannot resolve agent directory; set PI_CODING_AGENT_DIR")
		}
		agentDir = resolved
	}
	if cogitatorBin == "" {
		cogitatorBin = "cogitator"
	}

	extDir := filepath.Join(agentDir, "extensions")
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		return "", fmt.Errorf("omp: create extensions dir: %w", err)
	}

	content := strings.ReplaceAll(extensionTemplate, "__COGITATOR_BIN__", jsStringEscape(cogitatorBin))
	path := filepath.Join(extDir, ExtensionFilename)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", fmt.Errorf("omp: write extension: %w", err)
	}
	return path, nil
}

// jsStringEscape escapes a value for safe interpolation inside a double-quoted
// JS string literal (backslash and double-quote only; paths are single-line).
func jsStringEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}
