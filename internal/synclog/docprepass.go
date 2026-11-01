package synclog

// The secrets pre-pass: documents a joiner cannot do without, applied before the
// log's rows. A peer interleaves row and doc entries, so the joiner reaches a
// `$ref` fetch before the credentials unlocking it and stalls; this pass applies
// the named documents first. It moves no mark: the normal import later finds each
// a redelivery, which applyDoc already makes a no-op. The walk costs one extra
// pass over entries, not bodies: over-threshold rows travel as `$ref` stubs either
// way. Compacted origins are covered: the surviving entry per row sits below the
// floor, and the snapshot path applies the same survivors.

import (
	"fmt"
	"sort"
)

// PrepassDocs applies the named documents' entries from every other origin,
// newest-wins, without moving any mark. Locked is a no-op: an unverifiable entry
// is parked by the ordinary import, and this pass must not be the one path
// trusting it. An unfollowable origin, an unverifiable entry and a newer-schema
// writer are left to the import, which reports them.
func (i *Importer) PrepassDocs(names ...string) (int, error) {
	if len(i.key) == 0 || len(names) == 0 {
		return 0, nil
	}
	tables := make(map[string]bool, len(names))
	for _, name := range names {
		tables[DocPrefix+name] = true
	}
	state, err := i.transport.ReadMarks()
	if err != nil {
		return 0, fmt.Errorf("synclog: prepass: read the floors: %w", err)
	}
	if err := i.loadSigned(state); err != nil {
		return 0, err
	}
	origins := make([]string, 0, len(state.Heads))
	for origin := range state.Heads {
		if origin == i.db.Origin() {
			continue
		}
		origins = append(origins, origin)
	}
	sort.Strings(origins)
	var applied int
	for _, origin := range origins {
		n, err := i.prepassOrigin(origin, state.Heads[origin], tables)
		applied += n
		if err != nil {
			return applied, err
		}
	}
	return applied, nil
}

// prepassOrigin applies one origin's named documents; every refusal leaves the
// origin to the import.
func (i *Importer) prepassOrigin(origin string, head int, tables map[string]bool) (int, error) {
	if head <= 0 || i.badBoundary[origin] != nil {
		return 0, nil
	}
	docs, err := i.pullDocs(origin, head, tables)
	if err != nil {
		return 0, err
	}
	if len(docs) == 0 {
		return 0, nil
	}
	if _, held := i.held(docs); held {
		return 0, nil
	}
	if err := i.verify(origin, docs); err != nil {
		if holdsOrigin(err) {
			return 0, nil
		}
		return 0, err
	}
	return i.takeDocs(origin, docs)
}

// pullDocs reads one origin's entries to its head, keeping the asked-for tables.
// The transport is untrusted: foreign or out-of-range entries drop, and a
// non-advancing page ends the read rather than spinning.
func (i *Importer) pullDocs(origin string, head int, tables map[string]bool) ([]Entry, error) {
	var docs []Entry
	cursor := 0
	for cursor < head {
		page, err := i.transport.PullRange(origin, cursor, head)
		if err != nil {
			return nil, fmt.Errorf("synclog: prepass: pull %s: %w", origin, err)
		}
		advanced := cursor
		for _, e := range page {
			if e.Origin != origin || e.Seq <= cursor || e.Seq > head {
				continue
			}
			advanced = max(advanced, e.Seq)
			if tables[e.Table] {
				docs = append(docs, e)
			}
		}
		if advanced == cursor {
			break
		}
		cursor = advanced
	}
	return docs, nil
}
