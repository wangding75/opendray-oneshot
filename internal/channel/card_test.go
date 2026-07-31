package channel

import (
	"strings"
	"testing"
)

func TestCard_RenderText_Empty(t *testing.T) {
	var c *Card
	if got := c.RenderText(); got != "" {
		t.Fatalf("nil card: want empty, got %q", got)
	}
	c2 := &Card{}
	if got := c2.RenderText(); got != "" {
		t.Fatalf("empty card: want empty, got %q", got)
	}
}

func TestCard_RenderText_AllElements(t *testing.T) {
	card := &Card{
		Header: &CardHeader{Title: "Session idle"},
		Elements: []CardElement{
			CardMarkdown{Content: "Session **abc** went idle (silent for 60s)."},
			CardDivider{},
			CardActions{Buttons: [][]ButtonOption{
				{
					{Text: "Resume", Value: "cmd:/resume abc", Style: "primary"},
					{Text: "End", Value: "cmd:/cancel abc", Style: "danger"},
				},
			}},
			CardListItem{
				Text:   "Last reply: 14:32",
				Button: ButtonOption{Text: "Open", Value: "nav:/sessions/abc"},
			},
			CardSelect{
				Placeholder: "Switch model",
				Options:     []CardSelectOption{{Text: "Sonnet", Value: "sonnet"}, {Text: "Opus", Value: "opus"}},
			},
			CardNote{Text: "muted? /notify off"},
		},
	}
	out := card.RenderText()
	for _, want := range []string{
		"Session idle",
		"Session **abc** went idle",
		"──────────",
		"[Resume]",
		"[End]",
		"Last reply: 14:32  →  [Open]",
		"Switch model (options: Sonnet | Opus)",
		"muted? /notify off",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("RenderText() missing %q\n--full--\n%s", want, out)
		}
	}
}

func TestEncodeDecodeAction(t *testing.T) {
	cases := []struct {
		text     string
		wantOK   bool
		wantData string
	}{
		{"act:/resume abc", true, "/resume abc"},
		{"plain text", false, ""},
		{"", false, ""},
		{"act:", true, ""},
	}
	for _, c := range cases {
		got, ok := DecodeAction(c.text)
		if ok != c.wantOK || got != c.wantData {
			t.Errorf("DecodeAction(%q) = (%q,%v), want (%q,%v)", c.text, got, ok, c.wantData, c.wantOK)
		}
	}
	if got := EncodeAction("/resume abc"); got != "act:/resume abc" {
		t.Errorf("EncodeAction = %q", got)
	}
}
