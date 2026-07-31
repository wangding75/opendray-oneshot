package session

import (
	"regexp"
	"strings"

	"github.com/hinshun/vt10x"
)

// term.go: terminal-text utilities used to turn raw PTY output into a
// human-readable preview for downstream consumers (channel
// notifications, web UI excerpts). Ported from opendray v1's Telegram
// notification path.
//
// The cleaning pipeline is intentionally conservative — strip ANSI
// escapes and Unicode box-drawing decorations, drop ASCII control
// bytes that would render as garbage, then collapse runs of spaces.
// Heavy filtering (Claude-CLI chrome detection, dedupe, etc.) lives
// in v1 and can be ported later if the simple pipeline is too noisy.

var multiSpace = regexp.MustCompile(`[ ]{2,}`)

// StripANSI removes CSI / OSC / DCS / SS3 / G0-G1 escape sequences
// from s. Bytes outside the printable / TAB / LF range are also
// dropped (carriage returns become silent — CLI redraws would
// otherwise leave garbage).
func StripANSI(s string) string {
	out := make([]byte, 0, len(s))
	i := 0
	n := len(s)
	for i < n {
		b := s[i]
		if b == 0x1b { // ESC
			i++
			if i >= n {
				break
			}
			switch s[i] {
			case '[': // CSI
				i++
				for i < n && s[i] >= 0x30 && s[i] <= 0x3F {
					i++
				}
				for i < n && s[i] >= 0x20 && s[i] <= 0x2F {
					i++
				}
				if i < n && s[i] >= 0x40 && s[i] <= 0x7E {
					i++
				}
			case ']': // OSC — terminated by BEL or ST
				i++
				for i < n && s[i] != 0x07 && s[i] != 0x1b {
					i++
				}
				if i < n && s[i] == 0x07 {
					i++
				} else if i+1 < n && s[i] == 0x1b && s[i+1] == '\\' {
					i += 2
				}
			case 'P', 'X', '_', '^': // DCS / SOS / PM / APC
				i++
				for i+1 < n {
					if s[i] == 0x1b && s[i+1] == '\\' {
						i += 2
						break
					}
					i++
				}
			case '(', ')', '*', '+': // charset designators
				i++
				if i < n {
					i++
				}
			default:
				if s[i] >= 0x20 && s[i] <= 0x7E {
					i++
				}
			}
			continue
		}
		if b < 0x20 && b != '\n' && b != '\t' {
			i++
			continue
		}
		out = append(out, b)
		i++
	}
	return string(out)
}

// stripBoxDrawing replaces Unicode box-drawing characters with spaces
// (CLIs use them to draw frames; they look terrible in chat clients).
// Multi-space runs are collapsed.
func stripBoxDrawing(s string) string {
	var out strings.Builder
	out.Grow(len(s))
	for _, r := range s {
		if (r >= 0x2500 && r <= 0x25FF) ||
			r == '╭' || r == '╮' || r == '╯' || r == '╰' {
			out.WriteRune(' ')
			continue
		}
		out.WriteRune(r)
	}
	return multiSpace.ReplaceAllString(out.String(), " ")
}

// TailLines returns the last n non-empty trailing lines of s. Empty
// lines at the very end are trimmed so single-line responses don't
// turn into N-1 blank lines.
func TailLines(s string, n int) string {
	if n <= 0 {
		return ""
	}
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

// CleanTerminalOutput is the convenience pipeline used when the only
// thing we have is a raw byte stream (no virtual terminal): strip
// ANSI / box-drawing, normalise whitespace, then keep only the last
// `tailN` lines. Prefer ScreenSnapshot whenever a virtual terminal is
// available — it reflects what the user actually sees on screen.
func CleanTerminalOutput(raw string, tailN int) string {
	if raw == "" {
		return ""
	}
	cleaned := stripBoxDrawing(StripANSI(raw))
	// Drop trailing whitespace per line so cards don't have ragged ends.
	lines := strings.Split(cleaned, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	cleaned = strings.Join(lines, "\n")
	return strings.TrimSpace(TailLines(cleaned, tailN))
}

// ScreenSnapshot dumps the visible screen of a vt10x terminal as
// plain text. Each row is rendered with vt10x.View.Cell (so post-
// redraw state is what we get, not the raw byte history), trailing
// spaces are trimmed per row, and trailing blank rows are dropped so
// the snapshot ends right after the last meaningful line.
//
// Caller is responsible for nothing: ScreenSnapshot acquires the
// vt10x lock internally.
func ScreenSnapshot(vt vt10x.View) string {
	if vt == nil {
		return ""
	}
	vt.Lock()
	cols, rows := vt.Size()
	lines := make([]string, 0, rows)
	for y := 0; y < rows; y++ {
		row := make([]rune, 0, cols)
		for x := 0; x < cols; x++ {
			c := vt.Cell(x, y).Char
			if c == 0 {
				c = ' '
			}
			row = append(row, c)
		}
		lines = append(lines, strings.TrimRight(string(row), " \t"))
	}
	vt.Unlock()

	// Drop blank trailing rows so the snippet ends at the last visible
	// content, not 10 empty rows below the prompt.
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	// Collapse runs of 3+ blank rows in the middle (Claude's TUI
	// often leaves big gaps between the response and the prompt).
	out := make([]string, 0, len(lines))
	blankRun := 0
	for _, l := range lines {
		if l == "" {
			blankRun++
			if blankRun > 1 {
				continue
			}
		} else {
			blankRun = 0
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}
