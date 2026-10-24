package synclog

// One table's traffic: the log rows exported and imported under its name, with
// the body bytes each direction carried. Document entries are tallied under the
// table their entry names, so the breakdown answers which names dominate the
// wire rather than which storage API they arrived through.
type TableTally struct {
	ExportedEntries int   `json:"exported_entries"`
	ExportedBytes   int64 `json:"exported_bytes"`
	ImportedEntries int   `json:"imported_entries"`
	ImportedBytes   int64 `json:"imported_bytes"`
}

// TableTallies is the per-table breakdown one export, import or month carries.
// A nil map tallies nothing and merges as empty, so a path that moved no rows
// needs no initialization to report that.
type TableTallies map[string]TableTally

// noteExported folds entries this machine appended into the breakdown. Only
// entries a transport accepted are tallied: a refused append leaves its outbox
// rows for the next run, which tallies them when they land.
func (t *TableTallies) noteExported(entries []Entry) {
	t.note(entries, true)
}

// noteImported folds entries this machine applied into the breakdown. Entries
// a run dropped or held are not tallied: they moved no mark and will be
// offered again, which tallies them when they apply.
func (t *TableTallies) noteImported(entries []Entry) {
	t.note(entries, false)
}

// noteAppliedRows folds the row entries one batch write committed, skipping
// the entries the same write reported as dropped. A dropped entry was refused
// rather than applied: the mark moved past it instead of over it, so counting
// it would bill the breakdown for a row the file never took.
func (t *TableTallies) noteAppliedRows(rows []Entry, drops []Drop) {
	if len(drops) == 0 {
		t.noteImported(rows)
		return
	}
	dropped := make(map[int]bool, len(drops))
	for _, d := range drops {
		dropped[d.Seq] = true
	}
	var applied []Entry
	for _, e := range rows {
		if !dropped[e.Seq] {
			applied = append(applied, e)
		}
	}
	t.noteImported(applied)
}

func (t *TableTallies) note(entries []Entry, exported bool) {
	if len(entries) == 0 {
		return
	}
	if *t == nil {
		*t = make(TableTallies, len(entries))
	}
	for _, e := range entries {
		cur := (*t)[e.Table]
		if exported {
			cur.ExportedEntries++
			cur.ExportedBytes += int64(len(e.Body))
		} else {
			cur.ImportedEntries++
			cur.ImportedBytes += int64(len(e.Body))
		}
		(*t)[e.Table] = cur
	}
}

// Add folds another breakdown into this one, so a loop over batches -- or the
// steady attempt over its export and import -- reports one breakdown for the
// whole run.
func (t *TableTallies) Add(other TableTallies) {
	if len(other) == 0 {
		return
	}
	if *t == nil {
		*t = make(TableTallies, len(other))
	}
	for name, o := range other {
		cur := (*t)[name]
		cur.ExportedEntries += o.ExportedEntries
		cur.ExportedBytes += o.ExportedBytes
		cur.ImportedEntries += o.ImportedEntries
		cur.ImportedBytes += o.ImportedBytes
		(*t)[name] = cur
	}
}
