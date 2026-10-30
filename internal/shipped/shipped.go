// Package shipped answers whether a blob is one relevo shipped: the agent
// definitions and the daemon units each carry an index of every sha256 they
// ever shipped, so an install can tell a past release's bytes from a user
// edit. The two datasets live here because the parse and the query are one
// rule; each dataset's file moved with it.
package shipped

import (
	_ "embed"
	"strings"
	"sync"
)

// agentsSHA256 indexes every agent-definition blob relevo has ever shipped:
// one "<sha256>  <basename>" line per historical and working-tree version of
// every definition; scripts/agents-shipped.sh regenerates it.
//
//go:embed agents.sha256
var agentsSHA256 string

// unitsSHA256 indexes every unit blob relevo has ever shipped: one
// "<sha256>  <name>" line per historical and working-tree version of each
// source; scripts/serviceunit-shipped.sh regenerates it. Both units are
// indexed as templates, placeholders intact.
//
//go:embed units.sha256
var unitsSHA256 string

var (
	agentsOnce sync.Once
	agentsSets map[string]map[string]struct{}
	unitsOnce  sync.Once
	unitsSets  map[string]map[string]struct{}
)

// parseIndex parses data on first use. A malformed line is skipped, so one
// bad line can never abort an install: the index is quietly smaller, never
// an error.
func parseIndex(data string) map[string]map[string]struct{} {
	idx := make(map[string]map[string]struct{})
	for _, line := range strings.Split(data, "\n") {
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
	return idx
}

func agentsIndex() map[string]map[string]struct{} {
	agentsOnce.Do(func() {
		agentsSets = parseIndex(agentsSHA256)
	})
	return agentsSets
}

func unitsIndex() map[string]map[string]struct{} {
	unitsOnce.Do(func() {
		unitsSets = parseIndex(unitsSHA256)
	})
	return unitsSets
}

// Before reports whether sha is the sha256 of a version of the definition
// named doc that some past relevo shipped. A match means the file is a copy
// relevo wrote in an older release, not a user edit, so an install may
// refresh it.
func Before(doc, sha string) bool {
	if doc == "" || sha == "" {
		return false
	}
	_, ok := agentsIndex()[doc][sha]
	return ok
}

// UnitBefore reports whether sha is the sha256 of a version of the unit
// named doc that some relevo release shipped.
func UnitBefore(doc, sha string) bool {
	if doc == "" || sha == "" {
		return false
	}
	_, ok := unitsIndex()[doc][sha]
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
