package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

func (s *Store) Save(b Binding) error {
	return s.WithLock(func(tx *Tx) error { return tx.Save(b) })
}

func (s *Store) Load(name string) (Binding, error) {
	var b Binding
	err := s.read(name, func(tx *Tx) error {
		var err error
		b, err = tx.Load(name)
		return err
	})
	return b, err
}

func (s *Store) List() ([]Binding, error) {
	var bindings []Binding
	err := s.readAll(func(tx *Tx) error {
		var err error
		bindings, err = tx.List()
		return err
	})
	return bindings, err
}

func (s *Store) Archive(name string) (string, error) {
	var dest string

	err := s.WithLock(func(tx *Tx) error {
		var err error
		dest, err = tx.Archive(name)

		return err
	})
	if err != nil {
		return "", err
	}

	return dest, nil
}

func (s *Store) Delete(name string) error {
	return s.WithLock(func(tx *Tx) error { return tx.Delete(name) })
}

// FindByCWD returns the binding driving cwd, if any.
//
// A remote binding is never returned: its CWD is the repo the branch is cut
// from and results are fetched into, not a working tree it drives.
//
// A writer is preferred: a reader may share a writer's tree, and a verb run
// there means the writer's binding. With no writer, exactly one reader
// resolves to that reader; several readers are ambiguous, because there is no
// binding to prefer, and the caller must name one.
func (s *Store) FindByCWD(cwd string) (Binding, bool, error) {
	var writers, readers []Binding
	err := s.readAll(func(tx *Tx) error {
		bindings, err := tx.List()
		if err != nil {
			return err
		}
		for _, binding := range bindings {
			if binding.Builder.Remote() {
				continue
			}
			// A done binding no longer drives its tree: resolving `relevo
			// send` onto one would hand a plan to a finished session.
			if binding.CWD != cwd || binding.State == StateDone {
				continue
			}
			if isWriter(binding) {
				writers = append(writers, binding)
			} else {
				readers = append(readers, binding)
			}
		}
		return nil
	})
	if err != nil {
		return Binding{}, false, err
	}
	if len(writers) > 0 {
		return writers[0], true, nil
	}
	switch len(readers) {
	case 0:
		return Binding{}, false, nil
	case 1:
		return readers[0], true, nil
	default:
		return Binding{}, false, fmt.Errorf("several readers are bound to %s: pass --name: %w", cwd, ErrAmbiguousCWD)
	}
}

// read runs fn on a Tx. The database gives a reader a consistent snapshot, so
// no flock is needed.
//
// The name is refused here, before fn runs, because this is the one boundary
// every name-taking read shares and the name becomes a path in the sibling
// helpers; a name that cannot be a binding must not read as a missing one.
func (s *Store) read(name string, fn func(tx *Tx) error) error {
	if err := ValidName(name); err != nil {
		return err
	}
	return fn(&Tx{s: s})
}

// readAll is read's whole-root twin, for List and FindByCWD.
func (s *Store) readAll(fn func(tx *Tx) error) error {
	return fn(&Tx{s: s})
}

func (t *Tx) Save(b Binding) error {
	return t.s.save(b)
}

// SaveWithLog saves a binding and appends entries to its log in one database
// transaction: either all of it is written or none is. entries may be empty,
// in which case it is exactly Save. Each entry gets the next seq after the
// record's current maximum, in argument order.
func (t *Tx) SaveWithLog(b Binding, entries ...LogEntry) error {
	return t.s.saveWithLog(b, entries)
}

func (t *Tx) Load(name string) (Binding, error) {
	return t.s.load(name)
}

func (t *Tx) List() ([]Binding, error) {
	return t.s.list()
}

func (t *Tx) Delete(name string) error {
	return t.remove(name)
}

func (t *Tx) Archive(name string) (string, error) {
	return t.archive(name)
}

func (s *Store) save(b Binding) error {
	b, rec, err := s.prepareSave(b, nil)
	if err != nil {
		return err
	}
	d, err := s.dbForWrite()
	if err != nil {
		return err
	}
	if _, err := d.RecordPut(rec); err != nil {
		return fmt.Errorf("save binding %q: %w", b.Name, err)
	}
	return nil
}

// ensureBindingDir refuses a binding directory reached through anything but a
// real directory: the state root is runner-writable, so a symlink planted at
// <root>/<name> -- even one pointing at a real directory -- must not be
// followed. MkdirAll takes no flags, so the Lstat is the whole refusal here; it
// is why the check and the creation cannot be one call. An absent directory is
// created, which is how <root> and <root>/<name> come into being.
func (s *Store) ensureBindingDir(name string) error {
	dir := s.Dir(name)
	if fi, err := os.Lstat(dir); err == nil {
		if !fi.IsDir() {
			return fmt.Errorf("binding dir %s is not a directory", dir)
		}
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return os.MkdirAll(dir, bindingDirMode)
}

// prepareSave validates and stamps a binding and builds the record Save writes.
// siblings names the other bindings a create is about to write beside b, so a
// chain's members, none of which is stored yet, are exempt from the
// working-tree clash among themselves.
func (s *Store) prepareSave(b Binding, siblings []string) (Binding, db.Record, error) {
	// A binding written by a newer relevo is read-only for this binary: its
	// rewrite would erase every field this relevo does not know.
	if b.Format > BindingFormat {
		return b, db.Record{}, &ErrNewerFormat{Kind: "binding", Name: b.Name, Have: b.Format, Know: BindingFormat}
	}
	b.Format = storedFormat(recordFormat(b))
	// The empty actor is builder, and is stored as the literal "builder" so a
	// record always names the actor its runner plays.
	if b.Role == "" {
		b.Role = "builder"
	}
	// The empty shape is a writer: a reader is always named when it is bound.
	// Every record therefore carries a shape, at every format.
	if b.Shape == "" {
		b.Shape = ShapeWriter
	}

	if err := ValidName(b.Name); err != nil {
		return b, db.Record{}, err
	}
	if b.CWD == "" {
		return b, db.Record{}, errors.New("binding has no working directory")
	}

	if err := s.assertCWDFree(b, siblings); err != nil {
		return b, db.Record{}, err
	}

	now := time.Now().UTC()
	if b.CreatedAt.IsZero() {
		b.CreatedAt = now
	}
	b.UpdatedAt = now
	if b.RoundCap == 0 {
		b.RoundCap = DefaultRoundCap
	}
	if b.RoundTimeoutMS == 0 {
		b.RoundTimeoutMS = defaultRoundMSecs
	}

	if err := s.ensureBindingDir(b.Name); err != nil {
		return b, db.Record{}, fmt.Errorf("create binding dir: %w", err)
	}

	raw, err := json.Marshal(b)
	if err != nil {
		return b, db.Record{}, fmt.Errorf("marshal binding %q: %w", b.Name, err)
	}
	rec := db.Record{
		ID:        b.RecordID,
		Owner:     s.owner,
		Name:      b.Name,
		State:     string(b.State),
		Round:     b.Round,
		CWD:       b.CWD,
		JSON:      string(raw),
		CreatedAt: b.CreatedAt,
		UpdatedAt: b.UpdatedAt,
	}
	// The link is promoted out of the JSON into its own columns, the same way
	// the fields RecordPut's UPDATE and INSERT write are.
	if b.Link != nil {
		rec.LinkOrigin = b.Link.Installation
		rec.LinkID = b.Link.ID
	}
	return b, rec, nil
}

// saveWithLog saves the binding and appends entries in one transaction, so a
// failure anywhere leaves neither the record nor an entry behind.
func (s *Store) saveWithLog(b Binding, entries []LogEntry) error {
	b, rec, err := s.prepareSave(b, nil)
	if err != nil {
		return err
	}
	d, err := s.dbForWrite()
	if err != nil {
		return err
	}
	err = d.Tx(func(dtx *db.Tx) error {
		id, err := dtx.RecordPut(rec)
		if err != nil {
			return fmt.Errorf("save binding %q: %w", b.Name, err)
		}
		n, err := dtx.EventMaxSeq(id)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if n >= s.maxLog() {
				return fmt.Errorf("log exceeds %d entries", s.maxLog())
			}
			n++
			ev, err := encodeEvent(e, n)
			if err != nil {
				return err
			}
			if err := dtx.EventAppend(id, ev); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

// assertCWDFree refuses a second active writer on the same working tree.
//
// A remote binding is exempt on both sides: its CWD is never a working tree a
// builder writes in, so it may share a CWD with any number of remote bindings.
// Readers are exempt too: a reader leaves artifacts and never changes the
// tree, so it never blocks a writer and is never blocked by one.
//
// Members of one chain share the chain's single tree by design: the writer b,
// its already-stored siblings (found through chain_member) and the siblings a
// create is writing beside it (passed in) are all exempt. Every other writer
// on the tree is still refused, which is the point of the exemption.
func (s *Store) assertCWDFree(b Binding, siblings []string) error {
	if b.Builder.Remote() {
		return nil
	}
	if !isWriter(b) {
		return nil
	}
	exempt := map[string]bool{b.Name: true}
	for _, name := range siblings {
		exempt[name] = true
	}
	stored, err := s.chainMemberNames(b.Name)
	if err != nil {
		return err
	}
	for _, name := range stored {
		exempt[name] = true
	}
	bindings, err := s.list()
	if err != nil {
		return err
	}
	for _, other := range bindings {
		if other.Builder.Remote() {
			continue
		}
		if !isWriter(other) {
			continue
		}
		if other.CWD == b.CWD && !exempt[other.Name] && other.State != StateDone {
			return fmt.Errorf("%s is driven by binding %q (builder %s, round %d): %w",
				b.CWD, other.Name, other.BuilderCandidate, other.Round, ErrCWDTaken)
		}
	}
	return nil
}

// chainMemberNames returns the names of every binding of the chain that names
// name, in no particular order. A binding that is in no chain returns none, so
// a lone writer is exempt only from itself.
func (s *Store) chainMemberNames(name string) ([]string, error) {
	c, err := s.chainByMember(name)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	d, err := s.dbForRead()
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, nil
	}
	rows, err := d.ChainMembers(c.ID)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		names = append(names, row.Binding)
	}
	return names, nil
}

// isWriter reports whether b's actor changes its tree. An empty shape is a
// writer: every record written before A5 had no shape key.
func isWriter(b Binding) bool {
	return b.Shape != ShapeReader
}

func (s *Store) load(name string) (Binding, error) {
	d, err := s.dbForRead()
	if err != nil {
		return Binding{}, err
	}
	if d == nil {
		return Binding{}, fmt.Errorf("%s: %w", name, ErrNotFound)
	}
	rec, ok, err := d.RecordGet(s.owner, name)
	if err != nil {
		return Binding{}, fmt.Errorf("read binding %q: %w", name, err)
	}
	if !ok {
		return Binding{}, fmt.Errorf("%s: %w", name, ErrNotFound)
	}
	return decodeBinding([]byte(rec.JSON), name)
}

// decodeBinding turns a binding's JSON into a Binding.
func decodeBinding(raw []byte, name string) (Binding, error) {
	var b Binding
	if err := json.Unmarshal(raw, &b); err != nil {
		return Binding{}, fmt.Errorf("decode binding %q: %w", name, err)
	}

	// A binding written by a newer relevo is read-only for this binary: its
	// rewrite would erase every field this relevo does not know.
	if b.Format > BindingFormat {
		return Binding{}, &ErrNewerFormat{Kind: "binding", Name: name, Have: b.Format, Know: BindingFormat}
	}

	// "held" was a delivery in flight and "orphaned" meant the mastermind's
	// session had gone; both are simply active again now.
	switch b.State {
	case "held", "orphaned":
		slog.Debug("legacy binding state mapped to active", "binding", name, "state", string(b.State))
		b.State = StateActive
	}

	return b, nil
}

func (s *Store) list() ([]Binding, error) {
	d, err := s.dbForRead()
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, nil
	}
	recs, err := d.RecordList(s.owner)
	if err != nil {
		return nil, fmt.Errorf("read state root: %w", err)
	}

	bindings := make([]Binding, 0, len(recs))
	for _, rec := range recs {
		b, err := decodeBinding([]byte(rec.JSON), rec.Name)
		if err != nil {
			return nil, err
		}
		bindings = append(bindings, b)
	}

	return bindings, nil
}

// archive marks a binding's record archived and removes its directory, so the
// name frees for a fresh bind while its log and round files survive as rows.
// Every round is sealed first, ignoring Sealable: the caller has stopped the
// builder.
func (t *Tx) archive(name string) (string, error) {
	s := t.s
	if err := ValidName(name); err != nil {
		return "", err
	}
	// load is the ErrNotFound check.
	if _, err := s.load(name); err != nil {
		return "", err
	}

	if _, err := t.SealAll(name); err != nil {
		return "", err
	}

	d, err := s.dbForWrite()
	if err != nil {
		return "", err
	}
	if err := d.RecordArchive(s.owner, name, time.Now().UTC()); err != nil {
		return "", err
	}

	if err := os.RemoveAll(s.Dir(name)); err != nil {
		return "", fmt.Errorf("remove archived binding %q: %w", name, err)
	}

	return "", nil
}

// remove deletes a binding under the held lock: its record, its directory and,
// through the record's cascade, its events and sealed round files.
func (t *Tx) remove(name string) error {
	s := t.s
	if err := ValidName(name); err != nil {
		return err
	}
	d, err := s.dbForWrite()
	if err != nil {
		return err
	}
	_, ok, err := d.RecordGet(s.owner, name)
	if err != nil {
		return err
	}
	if ok {
		if _, err := t.SealAll(name); err != nil {
			return err
		}
		// The events and sealed files go with the row, through ON DELETE CASCADE.
		if err := d.RecordDelete(s.owner, name); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(s.Dir(name)); err != nil {
		return fmt.Errorf("delete binding %q: %w", name, err)
	}
	return nil
}
