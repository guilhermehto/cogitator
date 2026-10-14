//go:build !bundledplugin

package zellijplugin

// Bundled reports that this build carries no plugin wasm.
func Bundled() ([]byte, bool) {
	return nil, false
}
