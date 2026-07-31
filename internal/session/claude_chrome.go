package session

import (
	"regexp"
	"strings"
)

// claude_chrome.go: heuristic strip of Claude Code TUI chrome from a
// rendered screen snapshot, so notifications carry only the parts a
// human reader cares about (assistant messages, prompts, file lists).
// Ported from opendray v1's `filterClaudeChrome` and a separator-line
// drop. Each regex replaces a match with a single space — surrounding
// content stays put — and a second pass discards lines that became
// empty / pure-symbol / pure-separator.
//
// All filters are conservative: false-positives reach the user as a
// missing line, not as a wrongly-deleted one. When in doubt the line
// stays.

var chromePatterns = []*regexp.Regexp{
	// Model bar: "Opus 4.6", "Sonnet 4.6 | Max", "haiku 4.5..." (case-insensitive).
	regexp.MustCompile(`(?i)(?:pus|opus|sonnet|haiku)\s*4\.\d\S*(?:\s*[\|·]\s*\w+\]?)?`),
	// Permission hint + cycle-mode helper.
	regexp.MustCompile(`(?i)(?:bypass\s*permissions?\s*(?:on|off)?\s*)?(?:\(shift\+tab\s*(?:to\s*)?cycle\)|\bshift\+tabtocycle\b)`),
	regexp.MustCompile(`(?i)\bbypass\s*permissions?\s*(?:on|off)?\b`),
	// Esc-to-interrupt hint that travels with the spinner.
	regexp.MustCompile(`(?i)\besc(?:\s*to\s*)?interrupt\b`),
	// Expand hints: "(ctrl+o to expand)", "(ctrl+r to expand)".
	regexp.MustCompile(`(?i)(?:Listed\s+\d+\s+(?:directories|files)\s*)?\(ctrl\+[or]\s*to\s*expand\)`),
	// Token / cost counters in spinners ("↓ 323 tokens", "↑ 12k").
	regexp.MustCompile(`(?:↓|↑)\s*[\d,.]+\s*(?:tokens?|k)\b`),
	// Spinner glyphs prefixing transient labels (Cultivating / Thinking / Brewed / Pondering / Sautéed …).
	regexp.MustCompile(`[*✢⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏]\s*(?:Cultivating|Thinking|Generating|Reasoning|Connecting|Compacting|Reading|Searching|Executing|Streaming|Writing|Updating|Prestidigitatting|Cerebrating|Pondering|Brewed|Baked|Sautéed|Sauteed|Churned)[^)\n]*(?:\([^)]*\))?`),
	// Plain-text "Brewed for 15s" / "Baked for 1m 5s" without a leading glyph.
	regexp.MustCompile(`(?i)\b(?:Brewed|Baked|Sautéed|Sauteed|Churned|Pondering)\s+(?:for\s+)?[\dmsh\s\.]+`),
	// "thinking with xhigh effort" / "with high effort" badges.
	regexp.MustCompile(`(?i)\bthinking\s+with\s+\w*high\s+effort\b`),
	// Fancy prompt symbols.
	regexp.MustCompile(`❯+\s*`),
	regexp.MustCompile(`⏵+\s*`),
	regexp.MustCompile(`⏺\s*`),
	regexp.MustCompile(`▶+\s*`),
	regexp.MustCompile(`›\s*`),
	// Lone single-character prompts that survive with surrounding whitespace.
	regexp.MustCompile(`(?m)^\s*>\s*$`),
	// "copy" button label that some terminals leave on screen.
	regexp.MustCompile(`(?im)^\s*copy\s*$`),
}

// FilterClaudeChrome removes Claude-CLI chrome from `s` and drops
// lines that turned into pure debris. Designed to run AFTER ANSI
// stripping / box-drawing strip / screen snapshot — chrome here is
// the *textual* TUI labels left on the rendered screen.
func FilterClaudeChrome(s string) string {
	for _, re := range chromePatterns {
		s = re.ReplaceAllString(s, " ")
	}

	// Collapse internal multi-space runs introduced by the regex
	// substitutions so lines don't have ragged gaps.
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		t := multiSpace.ReplaceAllString(line, " ")
		t = strings.TrimRight(t, " \t")
		trimmed := strings.TrimSpace(t)
		if trimmed == "" {
			out = append(out, "")
			continue
		}
		if isSeparatorLine(trimmed) {
			continue
		}
		if !hasReadableContent(trimmed) {
			continue
		}
		// Lines reduced to a 1-3 character fragment after chrome
		// stripping are almost always debris.
		if len([]rune(trimmed)) <= 3 {
			continue
		}
		out = append(out, t)
	}

	// Collapse runs of 2+ blank lines down to 1 so the remaining
	// content packs tightly.
	collapsed := make([]string, 0, len(out))
	prevBlank := false
	for _, l := range out {
		if l == "" {
			if prevBlank {
				continue
			}
			prevBlank = true
		} else {
			prevBlank = false
		}
		collapsed = append(collapsed, l)
	}

	// Trim leading / trailing blank lines.
	for len(collapsed) > 0 && collapsed[0] == "" {
		collapsed = collapsed[1:]
	}
	for len(collapsed) > 0 && collapsed[len(collapsed)-1] == "" {
		collapsed = collapsed[:len(collapsed)-1]
	}
	return strings.Join(collapsed, "\n")
}

// isSeparatorLine reports whether s is composed entirely of
// box-drawing decorations (dashes, underscores, equals, tildes,
// asterisks, dots, spaces). Length ≤ 2 is considered "not a
// separator" to avoid clipping legitimate one-character content.
func isSeparatorLine(s string) bool {
	for _, r := range s {
		switch r {
		case ' ', '-', '=', '_', '~', '*', '.', '─', '━', '╌', '╍':
			continue
		default:
			return false
		}
	}
	return len(strings.TrimSpace(s)) > 2
}

// hasReadableContent reports whether s contains at least one
// alphanumeric or CJK character. Lines failing this check are pure
// punctuation / symbol noise.
func hasReadableContent(s string) bool {
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			// CJK Unified Ideographs + Extension A.
			(r >= 0x4E00 && r <= 0x9FFF) ||
			(r >= 0x3400 && r <= 0x4DBF) ||
			// Hiragana + Katakana.
			(r >= 0x3040 && r <= 0x30FF) ||
			// Hangul.
			(r >= 0xAC00 && r <= 0xD7A3) {
			return true
		}
	}
	return false
}
