// SPDX-License-Identifier: MIT
package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"io"
	"strings"
	"unicode/utf8"
)

type tdlTerminal struct {
	output      io.ReadCloser
	write       func([]byte) (int, error)
	interactive bool
}

type tdlLoginEvent struct {
	qr           string
	password     bool
	cursorReport bool
}

type tdlLoginResult struct {
	accepted bool
	label    string
	output   string
	issue    *Failure
}

const (
	tdlMaxConsoleLine = 4096
	tdlTerminalRows   = 64
	tdlTerminalCols   = 220
)

// tdl exposes its QR code as Unicode half-block terminal art. Parse complete
// frames in memory along with the two plain-text login status markers.
func readTDLLoginOutput(source io.Reader, publish func(tdlLoginEvent)) tdlLoginResult {
	stream := bufio.NewReader(source)
	var result tdlLoginResult
	var frame []string
	width := 0
	var line strings.Builder
	var csi strings.Builder
	password := false
	escape := byte(0)

	addLine := func() {
		value := line.String()
		line.Reset()
		if count := tdlQRWidth(value); count != 0 {
			if count != width {
				frame = nil
				width = count
			}
			frame = append(frame, value)
			if len(frame) == (width+1)/2 {
				if qr := tdlQRImage(frame, width); qr != "" {
					publish(tdlLoginEvent{qr: qr})
				}
				frame = nil
			}
			return
		}
		frame = nil
		width = 0
		if _, success := strings.CutPrefix(strings.TrimSpace(value), "Login successfully!"); success {
			result.accepted = true
			result.label = tdlUsername(value)
			return
		}
		// Some survey terminals leave a prompt on the same line when the
		// command prints its final result, even after erasing the line via CSI.
		if _, tail, ok := strings.Cut(value, "Login successfully! ID:"); ok {
			result.accepted = true
			result.label = tdlUsername(tail)
			return
		}
		if value != "" {
			if issue := tdlLoginIssue(value); issue != nil {
				result.issue = issue
			}
			result.output += value + "\n"
			if len(result.output) > tdlMaxConsoleLine {
				result.output = result.output[len(result.output)-tdlMaxConsoleLine:]
			}
		}
	}

	for {
		ch, _, err := stream.ReadRune()
		if err != nil {
			if line.Len() > 0 {
				addLine()
			}
			return result
		}
		switch escape {
		case 1: // ESC, awaiting an ANSI sequence type.
			switch ch {
			case '[':
				escape = 2
				csi.Reset()
			case ']':
				escape = 3
			default:
				escape = 0
			}
			continue
		case 2: // CSI, ending in the first byte in the final-byte range.
			if ch >= 0x40 && ch <= 0x7e {
				escape = 0
				if ch == 'K' {
					line.Reset()
				}
				if ch == 'n' && csi.String() == "6" {
					publish(tdlLoginEvent{cursorReport: true})
				}
			} else if csi.Len() < 8 {
				csi.WriteRune(ch)
			}
			continue
		case 3: // OSC, terminated by BEL or ST.
			if ch == '\a' {
				escape = 0
			} else if ch == '\x1b' {
				escape = 4
			}
			continue
		case 4:
			if ch == '\\' {
				escape = 0
			} else {
				escape = 3
			}
			continue
		}

		switch ch {
		case '\x1b':
			escape = 1
		case '\r':
			// PTYs can emit CRLF. Keep trailing spaces of QR lines intact.
		case '\n':
			addLine()
		default:
			if ch < 32 || line.Len() > tdlMaxConsoleLine {
				continue
			}
			line.WriteRune(ch)
			if !password && ch == ':' && strings.Contains(line.String(), "Enter 2FA Password:") {
				password = true
				publish(tdlLoginEvent{password: true})
			}
		}
	}
}

func tdlUsername(text string) string {
	_, tail, ok := strings.Cut(text, "Username:")
	if !ok {
		return ""
	}
	name := strings.TrimSpace(tail)
	if len(name) == 0 || len(name) > 32 {
		return ""
	}
	for _, ch := range name {
		if (ch < 'a' || ch > 'z') && (ch < 'A' || ch > 'Z') && (ch < '0' || ch > '9') && ch != '_' {
			return ""
		}
	}
	return "@" + name
}

func tdlQRWidth(line string) int {
	width := utf8.RuneCountInString(line)
	// A QR symbol of version 1 includes 21 modules and 8 quiet modules.
	if width < 29 || width > 125 || (width-29)%4 != 0 {
		return 0
	}
	for _, ch := range line {
		if ch != ' ' && ch != '█' && ch != '▀' && ch != '▄' {
			return 0
		}
	}
	return width
}

func tdlQRImage(frame []string, width int) string {
	// tdl draws dark modules with the empty character and uses block
	// glyphs for light modules. The final half-row is cropped to a square.
	modules := image.NewGray(image.Rect(0, 0, width, width))
	for pair, row := range frame {
		x, y := 0, pair*2
		for _, ch := range row {
			top, bottom := color.Gray{Y: 0}, color.Gray{Y: 0}
			if ch == '█' || ch == '▀' {
				top = color.Gray{Y: 255}
			}
			if ch == '█' || ch == '▄' {
				bottom = color.Gray{Y: 255}
			}
			modules.SetGray(x, y, top)
			if y+1 < width {
				modules.SetGray(x, y+1, bottom)
			}
			x++
		}
	}
	if !tdlValidQR(modules, width) {
		return ""
	}

	const scale = 6
	preview := image.NewGray(image.Rect(0, 0, width*scale, width*scale))
	for y := range width * scale {
		for x := range width * scale {
			preview.SetGray(x, y, modules.GrayAt(x/scale, y/scale))
		}
	}
	var output bytes.Buffer
	if err := png.Encode(&output, preview); err != nil {
		return ""
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(output.Bytes())
}

func tdlValidQR(qr *image.Gray, width int) bool {
	for y := range width {
		for x := range width {
			if (x < 4 || y < 4 || x >= width-4 || y >= width-4) && qr.GrayAt(x, y).Y != 255 {
				return false
			}
		}
	}
	for _, corner := range []image.Point{{X: 4, Y: 4}, {X: width - 11, Y: 4}, {X: 4, Y: width - 11}} {
		for y := range 7 {
			for x := range 7 {
				dark := x == 0 || x == 6 || y == 0 || y == 6 || (x >= 2 && x <= 4 && y >= 2 && y <= 4)
				if (qr.GrayAt(corner.X+x, corner.Y+y).Y == 0) != dark {
					return false
				}
			}
		}
	}
	return true
}
