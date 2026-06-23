# vhs script for capturing the README screenshot (tui.png).
#
# Usage (the tape runs ./cogitator, so the binary must be built first):
#   make demo
#   # or manually: go build -o ./cogitator ./cmd/cogitator && vhs demo.tape
#
# Produces:
#   tui.png  — static screenshot used in the README
#
# Install vhs:
#   brew install charmbracelet/tap/vhs
#   # or: go install github.com/charmbracelet/vhs@latest
#
# Font requirement:
#   vhs renders inside a headless browser (ttyd + xterm.js) and uses its own
#   font stack — it does NOT inherit your terminal's font. The TUI uses Nerd
#   Font glyphs (󰧞 󰋚 󰘥 󰌾 󰀦 󰐊 …); without a Nerd Font set here they render as
#   boxes. Install a Nerd Font on the host and reference it via `Set FontFamily`.
#
#   macOS (Homebrew):
#     brew install --cask font-hack-nerd-font
#   Linux: see https://www.nerdfonts.com/font-downloads (pick "Hack")
#
#   The family name in `Set FontFamily` must match the installed family exactly.
#   NOTE: use Hack (or another well-tested patched font). 0xProto Nerd Font's
#   glyphs do NOT rasterize in xterm.js's WebGL atlas — they render as boxes
#   here even though the font is installed and contains the codepoints.

Output tui.png

Set FontFamily "Hack Nerd Font"
Set FontSize 14
Set Width 1400
Set Height 820
Set Theme "Catppuccin Mocha"
Set Padding 20
Set TypingSpeed 40ms

# Hide/Show suppresses the build + launch so the final capture is the TUI
# alone, not the shell prompt + command line. The binary is gitignored, so it
# is built here first — a bare `vhs demo.tape` then works with no separate
# build step. Build frames happen during Hide, so the GIF is unaffected.
Hide
Type "go build -o ./cogitator ./cmd/cogitator"
Enter
Sleep 8s
Type "clear"
Enter
Type "./cogitator --demo"
Enter
# Give the TUI time to draw its first full frame (alt screen + initial
# snapshot delivery). Bump this if your machine is slow.
Sleep 2s
Show

Screenshot tui.png
Sleep 500ms
