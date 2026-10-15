package termpalette

import (
	"errors"
	"image/color"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestParseReadsReportedColoursAndSkipsTheRest(t *testing.T) {
	replies := "\x1b]4;1;rgb:bfbf/6161/6a6a\x07" +
		"\x1b]4;12;rgb:81/a1/c1\x1b\\" +
		"\x1b]4;99;rgb:ffff/ffff/ffff\x07" +
		"\x1b]4;3;rgb:zz/00/00\x07" +
		"\x1b[?62;22c"

	got := parse([]byte(replies))

	want := Palette{1: color.RGBA{R: 0xbf, G: 0x61, B: 0x6a, A: 0xff}, 12: color.RGBA{R: 0x81, G: 0xa1, B: 0xc1, A: 0xff}}
	if got != want {
		t.Errorf("parse = %v, want %v", got, want)
	}
}

func TestReadRepliesStopsAtDeviceAttributes(t *testing.T) {
	r := strings.NewReader("\x1b]4;1;rgb:bf/61/6a\x07\x1b[?62;22cq")

	replies, err := readReplies(r)
	if err != nil {
		t.Fatalf("readReplies: %v", err)
	}

	if string(replies) != "\x1b]4;1;rgb:bf/61/6a\x07\x1b[?62;22c" {
		t.Errorf("replies = %q, want them to end at the device attributes reply", replies)
	}
	if rest, _ := io.ReadAll(r); string(rest) != "q" {
		t.Errorf("unread input = %q, want the key press after the reply", rest)
	}
}

func TestReadRepliesReportsInputThatEndsBeforeTheReply(t *testing.T) {
	replies, err := readReplies(strings.NewReader("\x1b]4;1;rgb:bf/61/6a\x07"))

	if !errors.Is(err, io.EOF) {
		t.Errorf("err = %v, want io.EOF", err)
	}
	if parse(replies)[1] == nil {
		t.Error("colour read before the error was dropped")
	}
}

func TestQueryRejectsInputThatIsNotATerminal(t *testing.T) {
	in, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()

	if _, err := Query(in, in, time.Second); !errors.Is(err, errNotTerminal) {
		t.Errorf("err = %v, want errNotTerminal", err)
	}
}
