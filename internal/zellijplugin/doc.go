// Package zellijplugin carries the cogitator zellij plugin wasm inside
// release binaries. Release builds set the bundledplugin build tag after
// copying the built wasm here (make plugin-bundle); plain go build and go
// install leave it out, so building cogitator never needs a Rust toolchain.
package zellijplugin
