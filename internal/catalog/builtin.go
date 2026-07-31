package catalog

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

//go:embed builtin/*.json
var builtinFS embed.FS

// LoadBuiltin parses every builtin/*.json into a Manifest. Returns
// the manifest map plus a per-id sha256 hash (8 bytes hex) of the raw
// JSON for change detection in the providers table.
func LoadBuiltin() (map[string]Manifest, map[string]string, error) {
	entries, err := fs.ReadDir(builtinFS, "builtin")
	if err != nil {
		return nil, nil, fmt.Errorf("catalog: read builtin dir: %w", err)
	}

	manifests := make(map[string]Manifest)
	hashes := make(map[string]string)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		body, err := fs.ReadFile(builtinFS, "builtin/"+e.Name())
		if err != nil {
			return nil, nil, fmt.Errorf("catalog: read %s: %w", e.Name(), err)
		}
		var m Manifest
		if err := json.Unmarshal(body, &m); err != nil {
			return nil, nil, fmt.Errorf("catalog: parse %s: %w", e.Name(), err)
		}
		if m.ID == "" {
			return nil, nil, fmt.Errorf("catalog: manifest %s missing id", e.Name())
		}
		if _, dup := manifests[m.ID]; dup {
			return nil, nil, fmt.Errorf("catalog: duplicate manifest id %q", m.ID)
		}
		sum := sha256.Sum256(body)
		manifests[m.ID] = m
		hashes[m.ID] = hex.EncodeToString(sum[:8])
	}
	return manifests, hashes, nil
}

// SortedIDs returns the manifest IDs in lexical order — used to keep
// API output stable across requests.
func SortedIDs(manifests map[string]Manifest) []string {
	ids := make([]string, 0, len(manifests))
	for id := range manifests {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
