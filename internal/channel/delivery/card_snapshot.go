package delivery

import "github.com/opendray/opendray-v2/internal/channel"

// CardSnapshot is a JSON-safe representation of channel.Card. channel.Card
// stores interface-typed elements, so it cannot be reliably reconstructed from
// a raw JSON blob after a process restart without this tagged union.
type CardSnapshot struct {
	Header   *channel.CardHeader `json:"header,omitempty"`
	Elements []CardElement       `json:"elements,omitempty"`
}

type CardElement struct {
	Type     string                `json:"type"`
	Markdown *channel.CardMarkdown `json:"markdown,omitempty"`
	Actions  *channel.CardActions  `json:"actions,omitempty"`
	ListItem *channel.CardListItem `json:"list_item,omitempty"`
	Select   *channel.CardSelect   `json:"select,omitempty"`
	Note     *channel.CardNote     `json:"note,omitempty"`
}

func snapshotCard(card *channel.Card) *CardSnapshot {
	if card == nil {
		return nil
	}
	out := &CardSnapshot{Header: card.Header}
	for _, element := range card.Elements {
		switch value := element.(type) {
		case channel.CardMarkdown:
			copy := value
			out.Elements = append(out.Elements, CardElement{Type: "markdown", Markdown: &copy})
		case channel.CardDivider:
			out.Elements = append(out.Elements, CardElement{Type: "divider"})
		case channel.CardActions:
			copy := value
			out.Elements = append(out.Elements, CardElement{Type: "actions", Actions: &copy})
		case channel.CardListItem:
			copy := value
			out.Elements = append(out.Elements, CardElement{Type: "list_item", ListItem: &copy})
		case channel.CardSelect:
			copy := value
			out.Elements = append(out.Elements, CardElement{Type: "select", Select: &copy})
		case channel.CardNote:
			copy := value
			out.Elements = append(out.Elements, CardElement{Type: "note", Note: &copy})
		}
	}
	return out
}

func (snapshot *CardSnapshot) restore() *channel.Card {
	if snapshot == nil {
		return nil
	}
	card := &channel.Card{Header: snapshot.Header}
	for _, element := range snapshot.Elements {
		switch element.Type {
		case "markdown":
			if element.Markdown != nil {
				card.Elements = append(card.Elements, *element.Markdown)
			}
		case "divider":
			card.Elements = append(card.Elements, channel.CardDivider{})
		case "actions":
			if element.Actions != nil {
				card.Elements = append(card.Elements, *element.Actions)
			}
		case "list_item":
			if element.ListItem != nil {
				card.Elements = append(card.Elements, *element.ListItem)
			}
		case "select":
			if element.Select != nil {
				card.Elements = append(card.Elements, *element.Select)
			}
		case "note":
			if element.Note != nil {
				card.Elements = append(card.Elements, *element.Note)
			}
		}
	}
	return card
}
