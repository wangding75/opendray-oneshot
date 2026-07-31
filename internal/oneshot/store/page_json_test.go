package store

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPageJSONUsesFrozenControlPlaneFieldNames(t *testing.T) {
	raw, err := json.Marshal(Page[string]{
		Items:      []string{"item"},
		NextCursor: "opaque-cursor",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	if !strings.Contains(got, `"items":["item"]`) ||
		!strings.Contains(got, `"next_cursor":"opaque-cursor"`) {
		t.Fatalf("unexpected page JSON: %s", got)
	}
	if strings.Contains(got, "Items") || strings.Contains(got, "NextCursor") {
		t.Fatalf("Go field names leaked into REST response: %s", got)
	}
}
