// SPDX-License-Identifier: MIT
package main

import (
	"encoding/base64"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"rsc.io/qr"
)

func tdlFixtureFrame(t *testing.T, value string) (string, *qr.Code) {
	t.Helper()
	code, err := qr.Encode(value, qr.M)
	if err != nil {
		t.Fatal(err)
	}
	var frame strings.Builder
	for y := -4; y < code.Size+4; y += 2 {
		for x := -4; x < code.Size+4; x++ {
			topWhite, bottomWhite := !code.Black(x, y), y+1 < code.Size+4 && !code.Black(x, y+1)
			glyph := ' '
			switch {
			case topWhite && bottomWhite:
				glyph = '█'
			case topWhite:
				glyph = '▀'
			case bottomWhite:
				glyph = '▄'
			}
			frame.WriteRune(glyph)
		}
		frame.WriteString("\r\n")
	}
	return frame.String(), code
}

func TestTDLLoginTerminalFramesPreserveEveryModule(t *testing.T) {
	first, firstCode := tdlFixtureFrame(t, "fixture: first")
	second, secondCode := tdlFixtureFrame(t, "fixture: second")
	stream := "\x1b[34mScan QR code with your Telegram app...\x1b[0m\r\n" +
		first + strings.Repeat("\x1b[A", 10) + second + "\x1b[2K[?] Enter 2FA \x1b[1mPassword:\x1b[0m ****" +
		"\x1b[2KLogin successfully! ID: 7, Username: fixture_qa\r\n"
	var events []tdlLoginEvent
	got := readTDLLoginOutput(strings.NewReader(stream), func(event tdlLoginEvent) { events = append(events, event) })
	if !got.accepted || got.label != "@fixture_qa" || len(events) != 3 || !events[2].password {
		t.Fatalf("unexpected phases: success=%v label=%q events=%d", got.accepted, got.label, len(events))
	}
	if strings.Contains(got.output, "█") || strings.Contains(got.output, "fixture_qa") {
		t.Fatal("terminal result retained a QR frame or account details")
	}
	for i, code := range []*qr.Code{firstCode, secondCode} {
		url := events[i].qr
		prefix := "data:image/png;base64,"
		content, ok := strings.CutPrefix(url, prefix)
		if !ok {
			t.Fatal("missing PNG image")
		}
		decoded := base64.NewDecoder(base64.StdEncoding, strings.NewReader(content))
		img, err := png.Decode(decoded)
		if err != nil {
			t.Fatal(err)
		}
		size := (code.Size + 8) * 6
		if img.Bounds().Dx() != size || img.Bounds().Dy() != size {
			t.Fatal("missing QR rows or quiet zone")
		}
		for y := -4; y < code.Size+4; y++ {
			for x := -4; x < code.Size+4; x++ {
				got := color.GrayModel.Convert(img.At((x+4)*6+3, (y+4)*6+3)).(color.Gray).Y == 0
				want := code.Black(x, y)
				if got != want {
					t.Fatalf("QR module (%d,%d) black=%v, wanted %v", x, y, got, want)
				}
			}
		}
	}
}
