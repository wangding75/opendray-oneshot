package delivery

import "strings"

var defaultTextLimits = map[string]int{
	"telegram": 3800,
	"discord":  1900,
	"slack":    38000,
	"feishu":   28000,
	"dingtalk": 18000,
	"wecom":    18000,
	"wechat":   18000,
	"bridge":   3500,
}

func maxTextRunes(kind string) int {
	if limit := defaultTextLimits[kind]; limit > 0 {
		return limit
	}
	return 3500
}

// splitText preserves rune boundaries and prefers line breaks. Joining the
// returned parts reproduces the original text exactly.
func splitText(text string, limit int) []string {
	if limit <= 0 || len([]rune(text)) <= limit {
		return []string{text}
	}
	runes := []rune(text)
	parts := make([]string, 0, len(runes)/limit+1)
	for len(runes) > 0 {
		cut := limit
		if cut > len(runes) {
			cut = len(runes)
		}
		if cut < len(runes) {
			window := string(runes[:cut])
			if idx := strings.LastIndex(window, "\n"); idx >= limit/2 {
				cut = len([]rune(window[:idx+1]))
			}
		}
		parts = append(parts, string(runes[:cut]))
		runes = runes[cut:]
	}
	return parts
}
