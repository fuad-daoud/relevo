package synclog

// The secrets pre-pass: the documents a joiner cannot do without, applied
// before the log's rows rather than in the order they were written.
//
// The problem it solves is ordering, and the ordering is not ours to choose. A
// peer's log interleaves its row entries and its doc entries in sequence order,
// so a `doc:secrets` entry written after a row whose body lives in the object
// store is applied after that row. The importer applies one batch at a time and
// fetches a batch's bodies before it writes it, so the joiner reaches the fetch
// on the `$ref` long before the credentials that would let it succeed, and the
// whole origin stalls on an entry it could have unlocked a moment earlier.
//
// So the pre-pass walks each origin on its own, keeps only the entries naming
// the documents it was asked for, and applies those. It moves no import mark:
// the normal import reaches the same entries later and finds each of them a
// redelivery, which is what applyDoc already makes a no-op.
//
// What it costs is a walk, because the transport has no read of one table. Head
// answers with the latest entry per row as a table, key, sequence and digest
// with no body, and Pull hands back whole batches from a mark, so neither can
// hand back a document's value without carrying everything else with it. The
// walk therefore reads each origin's entries from its first sequence to its head
// and drops everything that is not a named document. On a remote the size of
// production that is one extra pass over the log's entries -- roughly the volume
// the join's own first import pass already reads -- in exchange for the join not
// stalling on the first body it cannot fetch. The bodies themselves are not the
// cost: a row over the threshold travels as a `$ref` stub, and a row under it
// travels inline whether or not this pass looks at it.
//
// The walk covers a compacted origin too. Compaction keeps the latest entry per
// row at or below its floor, and a doc entry is a row like any other, so the
// surviving `doc:secrets` entry for a name is still in the log below the floor
// and this pass reads it from the first sequence. It does not read the snapshot
// region for that separately: the snapshot path applies a region's own doc
// entries (see snapshotOrigin), so the two read the same surviving entries and
// each finds the other's application a no-op.

import (
	"fmt"
	"sort"
)

// PrepassDocs applies the entries of the named documents from every other
// origin, newest-wins, without moving any import mark. It answers with how many
// entries it applied.
//
// It is a no-op on a locked machine: an entry this machine cannot verify is
// parked by the ordinary import, and a pre-pass that trusted one would be the
// only path in this file that does.
//
// An origin this machine cannot follow is left to the import, which reports it
// properly: a pre-pass has no business turning one origin's refusal into the
// whole join's failure. The same goes for an entry that will not verify and for
// one whose writer is on a newer schema.
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

// prepassOrigin applies one origin's named documents and answers with what it
// applied. Every refusal leaves the origin to the import.
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

// pullDocs reads one origin's entries from its first sequence to its head and
// keeps the ones naming a table this pass was asked for.
//
// The transport is not trusted for the range: an entry from another origin, or
// one outside it, is dropped, and a page that does not advance ends the read
// rather than spinning.
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