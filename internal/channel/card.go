package channel

import (
	"fmt"
	"strings"
)

// Card is a structured rich message. Channel impls that implement
// CardSender render it natively (Telegram inline keyboard, Slack
// blocks, Feishu interactive card, ...). For platforms without card
// support the Hub falls back to RenderText() and ships the result via
// Channel.Send.
//
// Cards are intentionally narrow — they cover the small set of widgets
// every meaningful messaging platform supports (header / markdown /
// divider / buttons / list-item / select). Anything fancier should ship
// as a follow-up element type with a sensible RenderText fallback.
type Card struct {
	Header   *CardHeader   `json:"header,omitempty"`
	Elements []CardElement `json:"elements,omitempty"`
}

// CardHeader is the optional title bar of a card.
type CardHeader struct {
	Title string `json:"title"`
	// Color is one of: blue, green, red, orange, yellow, grey.
	// Adapters map to platform colors as best they can.
	Color string `json:"color,omitempty"`
}

// CardElement is the marker interface implemented by every card body
// element. Adapters type-switch on the concrete type.
type CardElement interface {
	cardElement()
	// RenderText is the plain-text fallback used when a Channel does
	// not implement CardSender. Should be terse — one line per element
	// where possible.
	RenderText() string
}

// CardMarkdown renders markdown-formatted text.
type CardMarkdown struct {
	Content string `json:"content"`
}

// CardDivider renders a horizontal rule.
type CardDivider struct{}

// CardActions renders a row (or multiple rows) of clickable buttons.
type CardActions struct {
	// Buttons is a 2D slice — each inner slice is one row.
	Buttons [][]ButtonOption `json:"buttons"`
}

// CardListItem renders a row with descriptive text on the left and a
// single action button on the right.
type CardListItem struct {
	Text   string       `json:"text"`
	Button ButtonOption `json:"button"`
}

// CardSelect renders a dropdown selector.
type CardSelect struct {
	Placeholder string             `json:"placeholder,omitempty"`
	InitValue   string             `json:"init_value,omitempty"`
	Options     []CardSelectOption `json:"options"`
	// CallbackPrefix is prepended to the chosen option's value when
	// emitted as an inbound action (e.g. "select:model:" + "opus-4").
	CallbackPrefix string `json:"callback_prefix,omitempty"`
}

// CardSelectOption is one item in a CardSelect dropdown.
type CardSelectOption struct {
	Text  string `json:"text"`
	Value string `json:"value"`
}

// CardNote renders small footer text below the body.
type CardNote struct {
	Text string `json:"text"`
}

func (CardMarkdown) cardElement() {}
func (CardDivider) cardElement()  {}
func (CardActions) cardElement()  {}
func (CardListItem) cardElement() {}
func (CardSelect) cardElement()   {}
func (CardNote) cardElement()     {}

func (m CardMarkdown) RenderText() string { return m.Content }
func (CardDivider) RenderText() string    { return "──────────" }
func (a CardActions) RenderText() string {
	var labels []string
	for _, row := range a.Buttons {
		for _, b := range row {
			labels = append(labels, "["+b.Text+"]")
		}
	}
	return strings.Join(labels, " ")
}
func (li CardListItem) RenderText() string {
	return li.Text + "  →  [" + li.Button.Text + "]"
}
func (s CardSelect) RenderText() string {
	opts := make([]string, len(s.Options))
	for i, o := range s.Options {
		opts[i] = o.Text
	}
	return s.Placeholder + " (options: " + strings.Join(opts, " | ") + ")"
}
func (n CardNote) RenderText() string { return n.Text }

// ButtonOption is one clickable inline button. Value is the opaque
// callback payload that gets routed back as an inbound action when the
// user clicks (Telegram caps callback_data at 64 bytes — keep Value
// short).
type ButtonOption struct {
	Text  string `json:"text"`
	Value string `json:"value"`
	// Style is "primary" | "default" | "danger". Adapters map this to
	// platform-specific styling; the default is "default".
	Style string `json:"style,omitempty"`
}

// RenderText produces a plain-text fallback view of the entire card.
// Used by the Hub when the Channel does not implement CardSender.
func (c *Card) RenderText() string {
	if c == nil {
		return ""
	}
	var lines []string
	if c.Header != nil && c.Header.Title != "" {
		lines = append(lines, "*"+c.Header.Title+"*")
	}
	for _, el := range c.Elements {
		t := strings.TrimSpace(el.RenderText())
		if t != "" {
			lines = append(lines, t)
		}
	}
	return strings.Join(lines, "\n")
}

// CardActionPrefix is the prefix the Hub uses when a card-button click
// is encoded into a ChannelMessage.Text inbound event. Adapters that
// receive callback_query payloads should prepend this prefix unless
// they wrap the callback in metadata (preferred).
const CardActionPrefix = "act:"

// EncodeAction turns a button Value into the inbound text marker the
// Hub recognises. Used by adapters that surface callbacks via Text.
func EncodeAction(value string) string { return CardActionPrefix + value }

// DecodeAction strips CardActionPrefix and returns (action, true) when
// the input was a button click; otherwise ("", false).
func DecodeAction(text string) (string, bool) {
	if !strings.HasPrefix(text, CardActionPrefix) {
		return "", false
	}
	return strings.TrimPrefix(text, CardActionPrefix), true
}

// String makes Card friendly to %s in logs.
func (c *Card) String() string {
	if c == nil {
		return "<nil card>"
	}
	header := ""
	if c.Header != nil {
		header = c.Header.Title
	}
	return fmt.Sprintf("Card{header=%q, elements=%d}", header, len(c.Elements))
}
