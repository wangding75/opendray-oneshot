package telegram

import (
	"strings"
	"testing"
)

func TestFormatForTelegram_BoldItalicCode(t *testing.T) {
	in := "Some **bold** and *italic* and `inline code` text"
	out := formatForTelegram(in)
	if !strings.Contains(out, "<b>bold</b>") {
		t.Errorf("bold not converted:\n%s", out)
	}
	if !strings.Contains(out, "<i>italic</i>") {
		t.Errorf("italic not converted:\n%s", out)
	}
	if !strings.Contains(out, "<code>inline code</code>") {
		t.Errorf("inline code not converted:\n%s", out)
	}
}

func TestFormatForTelegram_FencedCodeBlock(t *testing.T) {
	in := "Before\n```\nfunc main() {}\n```\nAfter"
	out := formatForTelegram(in)
	if !strings.Contains(out, "<pre>") || !strings.Contains(out, "</pre>") {
		t.Errorf("code fence not wrapped:\n%s", out)
	}
	if !strings.Contains(out, "func main() {}") {
		t.Errorf("code body lost:\n%s", out)
	}
}

func TestFormatForTelegram_HeadingsAsBold(t *testing.T) {
	in := "# Top Heading\n## Subheading\nbody"
	out := formatForTelegram(in)
	for _, want := range []string{"<b>Top Heading</b>", "<b>Subheading</b>"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestFormatForTelegram_Bullets(t *testing.T) {
	in := "- first\n- second\n* third"
	out := formatForTelegram(in)
	if strings.Count(out, "  • ") != 3 {
		t.Errorf("expected 3 bullet rows, got:\n%s", out)
	}
}

func TestFormatForTelegram_TableToVertical(t *testing.T) {
	in := strings.Join([]string{
		"| 功能 | 状态 | 建议 |",
		"|---|---|---|",
		"| Quote 报价单 | 有 Model 无 UI | ❌ 砍掉 |",
		"| Reminder 提醒 | 完整未实现 | ⚠️ 简化 |",
	}, "\n")
	out := formatForTelegram(in)
	// Should NOT contain the malformed pipe-mash:
	if strings.Contains(out, "| 功能 |") {
		t.Errorf("raw table row leaked:\n%s", out)
	}
	for _, want := range []string{
		"<b>功能:</b> Quote 报价单",
		"<b>建议:</b>",
		"<b>状态:</b> 完整未实现",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestFormatForTelegram_DropsSeparatorRules(t *testing.T) {
	in := "before\n---\n_______________\n===\nafter"
	out := formatForTelegram(in)
	for _, gone := range []string{"---", "___", "==="} {
		if strings.Contains(out, gone) {
			t.Errorf("separator rule %q leaked:\n%s", gone, out)
		}
	}
	if !strings.Contains(out, "before") || !strings.Contains(out, "after") {
		t.Errorf("real content lost:\n%s", out)
	}
}

func TestFormatForTelegram_EscapesHTML(t *testing.T) {
	in := "Use <script> tags & be safe"
	out := formatForTelegram(in)
	if strings.Contains(out, "<script>") {
		t.Errorf("script tag not escaped:\n%s", out)
	}
	for _, want := range []string{"&lt;script&gt;", "&amp;"} {
		if !strings.Contains(out, want) {
			t.Errorf("entity %q missing in:\n%s", want, out)
		}
	}
}

func TestFormatForTelegram_AssistantBulletBolded(t *testing.T) {
	in := "● Write(docs/X.md)\n  └ Wrote 3 lines\n● 已完成"
	out := formatForTelegram(in)
	// The leading bullet glyphs should be wrapped in <b> for emphasis.
	for _, want := range []string{"<b>●</b>", "<b>└</b>"} {
		if !strings.Contains(out, want) {
			t.Errorf("bullet %q not bolded in:\n%s", want, out)
		}
	}
}

func TestRebalanceHTMLChunks_PreSplitAcrossChunks(t *testing.T) {
	// Chunk A opens <pre>, doesn't close. Chunk B has the close.
	chunks := []string{
		"<pre>line1\nline2",
		"line3</pre>\nafter",
	}
	out := rebalanceHTMLChunks(chunks)
	if !strings.HasSuffix(out[0], "</pre>") {
		t.Errorf("chunk 0 should close pre:\n%s", out[0])
	}
	if !strings.HasPrefix(out[1], "<pre>") {
		t.Errorf("chunk 1 should reopen pre:\n%s", out[1])
	}
}

func TestRebalanceHTMLChunks_NoOpenBlocks(t *testing.T) {
	chunks := []string{"<b>hi</b>", "<i>there</i>"}
	out := rebalanceHTMLChunks(chunks)
	if out[0] != chunks[0] || out[1] != chunks[1] {
		t.Errorf("chunks without <pre> should be unchanged: %v", out)
	}
}

func TestIsSeparatorRunLine(t *testing.T) {
	cases := []struct {
		s    string
		want bool
	}{
		{"---", true},
		{"_____________", true},
		{"=== === ===", true},
		{"--", false}, // too short
		{"-- a --", false},
		{"hello world", false},
	}
	for _, c := range cases {
		if got := isSeparatorRunLine(c.s); got != c.want {
			t.Errorf("isSeparatorRunLine(%q) = %v, want %v", c.s, got, c.want)
		}
	}
}
