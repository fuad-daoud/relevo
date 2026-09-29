package harness

import (
	_ "embed"
	"strings"
	"sync"
)

// shippedSHA256 indexes every agent-definition blob relevo has ever shipped:
// one "<sha256>  <basename>" line per historical and working-tree version of
// every file under agents/; scripts/agents-shipped.sh regenerates it.
//
//go:embed agents/shipped.sha256
var shippedSHA256 string

var (
	shippedOnce sync.Once
	shippedSets map[string]map[string]struct{}
)

// shippedIndex parses shippedSHA256 on first use. A malformed line is skipped,
// so one bad line can never abort an install: the index is quietly smaller,
// never an error.
func shippedIndex() map[string]map[string]struct{} {
	shippedOnce.Do(func() {
		idx := make(map[string]map[string]struct{})
		for _, line := range strings.Split(shippedSHA256, "\n") {
			fields := strings.Fields(line)
			if len(fields) != 2 || !isLowerHex64(fields[0]) {
				continue
			}
			sha, doc := fields[0], fields[1]
			set, ok := idx[doc]
			if !ok {
				set = make(map[string]struct{})
				idx[doc] = set
			}
			set[sha] = struct{}{}
		}
		shippedSets = idx
	})
	return shippedSets
}

// ShippedBefore reports whether sha is the sha256 of a version of the
// definition named doc that some past relevo shipped. A match means the file
// is a copy relevo wrote in an older release, not a user edit, so an install
// may refresh it.
func ShippedBefore(doc, sha string) bool {
	if doc == "" || sha == "" {
		return false
	}
	_, ok := shippedIndex()[doc][sha]
	return ok
}

func isLowerHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
