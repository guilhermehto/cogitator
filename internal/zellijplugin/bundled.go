//go:build bundledplugin

package zellijplugin

import _ "embed"

//go:embed cogitator-zellij.wasm
var wasm []byte

// Bundled returns the embedded plugin wasm.
func Bundled() ([]byte, bool) {
	return wasm, true
}
