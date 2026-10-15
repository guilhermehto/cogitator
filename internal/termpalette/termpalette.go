// Package termpalette asks the terminal for the RGB value of its 16 theme
// colours (OSC 4), so styled text can be adjusted without leaving the theme.
package termpalette

import (
	"errors"
	"fmt"
	"image/color"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
	"github.com/muesli/cancelreader"
)

// Palette holds the theme colours by index. A nil entry is a colour the
// terminal did not report.
type Palette [16]color.Color

var (
	colourReply      = regexp.MustCompile(`\x1b\]4;(\d+);(rgba?:[0-9A-Fa-f/]+)(?:\x07|\x1b\\)`)
	deviceAttributes = regexp.MustCompile(`\x1b\[\?[0-9;]*c$`)

	errNotTerminal = errors.New("termpalette: input is not a terminal")
)

// Query asks the terminal behind in and out for its theme colours and waits at
// most timeout for the replies. A primary device attributes query follows the
// colour queries: terminals answer it even when they ignore OSC 4, so its
// reply ends the wait.
func Query(in, out *os.File, timeout time.Duration) (Palette, error) {
	if !term.IsTerminal(in.Fd()) {
		return Palette{}, errNotTerminal
	}

	state, err := term.MakeRaw(in.Fd())
	if err != nil {
		return Palette{}, fmt.Errorf("termpalette: raw mode: %w", err)
	}
	defer func() { _ = term.Restore(in.Fd(), state) }()

	reader, err := cancelreader.NewReader(in)
	if err != nil {
		return Palette{}, fmt.Errorf("termpalette: reader: %w", err)
	}
	defer func() { _ = reader.Close() }()

	if _, err := io.WriteString(out, query()); err != nil {
		return Palette{}, fmt.Errorf("termpalette: write query: %w", err)
	}

	timer := time.AfterFunc(timeout, func() { reader.Cancel() })
	defer timer.Stop()

	replies, err := readReplies(reader)

	return parse(replies), err
}

func query() string {
	var q strings.Builder
	for i := range len(Palette{}) {
		fmt.Fprintf(&q, "\x1b]4;%d;?\x07", i)
	}
	q.WriteString(ansi.RequestPrimaryDeviceAttributes)

	return q.String()
}

// readReplies reads up to the device attributes reply. It reads a byte at a
// time so key presses that follow the reply stay for the program.
func readReplies(r io.Reader) ([]byte, error) {
	var replies []byte
	b := make([]byte, 1)
	for !deviceAttributes.Match(replies) {
		n, err := r.Read(b)
		replies = append(replies, b[:n]...)
		if err != nil {
			return replies, fmt.Errorf("termpalette: read reply: %w", err)
		}
	}

	return replies, nil
}

func parse(replies []byte) Palette {
	var p Palette
	for _, m := range colourReply.FindAllSubmatch(replies, -1) {
		i, err := strconv.Atoi(string(m[1]))
		if err != nil || i >= len(p) {
			continue
		}
		p[i] = ansi.XParseColor(string(m[2]))
	}

	return p
}
