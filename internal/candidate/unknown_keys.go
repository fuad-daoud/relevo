package candidate

import (
	"encoding/json"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/fuad-daoud/relevo/internal/jsonshape"
)

func KnownFields() []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range jsonshape.Keys(reflect.TypeOf(Candidate{})) {
		top := l
		if i := strings.IndexAny(l, ".[{"); i >= 0 {
			top = l[:i]
		}
		if !seen[top] {
			seen[top] = true
			out = append(out, top)
		}
	}
	sort.Strings(out)
	return out
}

// UnknownKeys returns the key paths in body that Candidate's JSON shape does
// not declare, as <name>.<key> with nested keys dotted on; an entry without a
// name is addressed by its index.
func UnknownKeys(body []byte) []string {
	var rows []map[string]any
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil
	}
	leaves := jsonshape.Keys(reflect.TypeOf(Candidate{}))
	leafSet := make(map[string]bool, len(leaves))
	for _, l := range leaves {
		leafSet[l] = true
	}

	var paths []string
	for i, row := range rows {
		name := strconv.Itoa(i)
		if n, ok := row["name"].(string); ok && n != "" {
			name = n
		}
		walkUnknown(name, "", row, leaves, leafSet, &paths)
	}
	sort.Strings(paths)
	return paths
}

func walkUnknown(name, prefix string, v any, leaves []string, leafSet map[string]bool, paths *[]string) {
	switch val := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			child := k
			if prefix != "" {
				child = prefix + "." + k
			}
			if leafSet[child] || hasPrefix(leaves, child, ".") || hasPrefix(leaves, child, "[]") || hasPrefix(leaves, child, "{}") {
				walkUnknown(name, child, val[k], leaves, leafSet, paths)
				continue
			}
			*paths = append(*paths, name+"."+child)
		}
	case []any:
		for _, item := range val {
			walkUnknown(name, prefix+"[]", item, leaves, leafSet, paths)
		}
	}
}

func hasPrefix(leaves []string, p, sep string) bool {
	pre := p + sep
	for _, l := range leaves {
		if strings.HasPrefix(l, pre) {
			return true
		}
	}
	return false
}
