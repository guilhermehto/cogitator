# vhs script for capturing a README screenshot / demo GIF.
#
# Usage:
#   go build -o ./cogitator ./cmd/cogitator
#   vhs demo.tape
#
# Produces:
#   tui.gif  — animated capture (single steady frame here)
#   tui.png  — static screenshot (use this in the README)
#
# Install vhs:
#   brew install charmbracelet/tap/vhs
#   # or: go install github.com/charmbracelet/vhs@latest
#
# Font requirement:
#   vhs renders inside a headless browser and uses its own font stack — it
#   does NOT inherit your terminal's font. The TUI uses Nerd Font glyphs
#   (󰧞 󰋚 󰘥 󰌾 󰀦 󰐊 …); without a Nerd Font set here they render as boxes.
#   Install a Nerd Font on the host and reference it via `Set Font` below.
#
#   macOS (Homebrew):
#     brew install --cask font-0xproto-nerd-font
#   Linux: see https://www.nerdfonts.com/font-downloads (pick "0xProto")
#
#   The font family name in `Set FontFamily` must match the installed
#   family exactly. Swap the value below if you use a different Nerd Font.

Output tui.gif
Output tui.png

Set FontFamily "0xProto Nerd Font"
Set FontSize 14
Set Width 1400
Set Height 820
Set Theme "Catppuccin Mocha"
Set Padding 20
Set TypingSpeed 40ms

# Hide/Show suppresses the typing of the launch command so the final
# capture is the TUI alone, not the shell prompt + command line.
Hide
Type "./cogitator --demo"
Enter
# Give the TUI time to draw its first full frame (alt screen + initial
# snapshot delivery). Bump this if your machine is slow.
Sleep 2s
Show

Screenshot tui.png
Sleep 500ms
