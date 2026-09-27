package mastermind

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
)

// mastermindKeyPrefix is the kv prefix every record's row shares. Migration
// 008 rewrites the historical "planner/" rows to this prefix.
const mastermindKeyPrefix = "mastermind/"

func registryKey(id string) string { return mastermindKeyPrefix + id }

// lockFileName is the pre-database registry's lock file, removed by the
// import.
const lockFileName = ".lock"

// seenRefreshInterval is how stale seen_at must be before Touch rewrites it.
const seenRefreshInterval = time.Minute

// Registry owns the mastermind records.
type Registry interface {
	Get(id string) (Record, error)
	ByName(name string) (Record, error)
	BySession(kind, sessionID string) (Record, error)
	ByHost(pid int, startedAt int64) (Record, error)
	List() ([]Record, error)
	Create(r Record) (Record, error)
	MoveSession(id, sessionID, transcript string, now time.Time) (Record, error)
	SetHost(id string, pid int, startedAt int64) (Record, error)
	Rename(id, name string) (Record, error)
	Touch(id string, now time.Time) error
	// Forget removes a record; inUse refuses with ErrInUse for a non-DONE
	// binding, nil refuses nothing.
	Forget(id string, inUse func(id string) bool) error
}

// DBRegistry is relevo's registry over the store root database's kv rows.
// Every mutating method runs inside one DBTxKV.Tx, so two hook firings
// serialise: whichever runs second sees the first's record.
type DBRegistry struct {
	KV db.DBTxKV

	// Now supplies the clock for records this registry writes. Nil means
	// time.Now; tests pin it.
	Now func() time.Time

	// Root is the pre-database masterminds directory the import reads once.
	// "" imports nothing.
	Root string

	importOnce sync.Once
	importErr  error
}

var _ Registry = (*DBRegistry)(nil)

func (r *DBRegistry) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// ensureImported adopts the pre-database record files once per registry: each
// <Root>/*.json is put to mastermind/<id> and removed, then the lock file and an
// emptied directory go too. A malformed file fails loudly and stays put; a
// missing Root is a no-op.
func (r *DBRegistry) ensureImported() error {
	if r.Root == "" {
		return nil
	}
	r.importOnce.Do(func() { r.importErr = r.importFiles() })
	return r.importErr
}

func (r *DBRegistry) importFiles() error {
	entries, err := os.ReadDir(r.Root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("mastermind: read registry root: %w", err)
	}

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".json") {
			continue
		}
		id := strings.TrimSuffix(name, ".json")
		if _, _, err := db.KVImportFile(r.KV, registryKey(id), filepath.Join(r.Root, name)); err != nil {
			return err
		}
	}

	// A directory still holding .agy/ is not empty, so it stays.
	_ = os.Remove(filepath.Join(r.Root, lockFileName))
	_ = os.Remove(r.Root)
	return nil
}

// ops is initLocked's lock-free view of this registry over one transaction.
func (r *DBRegistry) ops(kv db.KVTx) regOps { return kvOps{reg: r, kv: kv} }

type kvOps struct {
	reg *DBRegistry
	kv  db.KVTx
}

func (o kvOps) byName(name string) (Record, error) { return o.reg.byNameFrom(o.kv, name) }

func (o kvOps) byHost(pid int, startedAt int64) (Record, error) {
	return o.reg.byHostFrom(o.kv, pid, startedAt)
}

func (o kvOps) bySession(kind, sessionID string) (Record, error) {
	return o.reg.bySessionFrom(o.kv, kind, sessionID)
}

func (o kvOps) create(r Record) (Record, error) { return o.reg.createIn(o.kv, r) }

func (o kvOps) moveSession(id, sessionID, transcript string, now time.Time) (Record, error) {
	return o.reg.moveSessionIn(o.kv, id, sessionID, transcript, now)
}

func (o kvOps) setHost(id string, pid int, startedAt int64) (Record, error) {
	return o.reg.setHostIn(o.kv, id, pid, startedAt)
}

func (o kvOps) rename(id, name string) (Record, error) { return o.reg.renameIn(o.kv, id, name) }

func (o kvOps) touchForced(id string, now time.Time) (Record, error) {
	return o.reg.touchForcedIn(o.kv, id, now)
}

// decodeRecord decodes one row's document, naming its key on failure.
func decodeRecord(key string, raw []byte) (Record, error) {
	var rec Record
	if err := json.Unmarshal(raw, &rec); err != nil {
		return Record{}, fmt.Errorf("mastermind: decode %s: %w", key, err)
	}
	return rec, nil
}

// Get returns the record with that id, or ErrNotFound.
func (r *DBRegistry) Get(id string) (Record, error) {
	if err := r.ensureImported(); err != nil {
		return Record{}, err
	}
	return r.getFrom(r.KV, id)
}

func (r *DBRegistry) getFrom(kv db.KVTx, id string) (Record, error) {
	if ValidID(id) != nil {
		return Record{}, fmt.Errorf("%s: %w", id, ErrNotFound)
	}
	raw, ok, err := kv.KVGet(registryKey(id))
	if err != nil {
		return Record{}, fmt.Errorf("mastermind: read %s: %w", registryKey(id), err)
	}
	if !ok {
		return Record{}, fmt.Errorf("%s: %w", id, ErrNotFound)
	}
	return decodeRecord(registryKey(id), raw)
}

// ByName returns the record with that name, or ErrNotFound.
func (r *DBRegistry) ByName(name string) (Record, error) {
	if err := r.ensureImported(); err != nil {
		return Record{}, err
	}
	return r.byNameFrom(r.KV, name)
}

func (r *DBRegistry) byNameFrom(kv db.KVTx, name string) (Record, error) {
	records, err := r.listFrom(kv)
	if err != nil {
		return Record{}, err
	}
	for _, rec := range records {
		if rec.Name == name {
			return rec, nil
		}
	}
	return Record{}, fmt.Errorf("%s: %w", name, ErrNotFound)
}

// BySession returns the record whose current session is (kind, sessionID).
func (r *DBRegistry) BySession(kind, sessionID string) (Record, error) {
	if err := r.ensureImported(); err != nil {
		return Record{}, err
	}
	return r.bySessionFrom(r.KV, kind, sessionID)
}

func (r *DBRegistry) bySessionFrom(kv db.KVTx, kind, sessionID string) (Record, error) {
	records, err := r.listFrom(kv)
	if err != nil {
		return Record{}, err
	}
	for _, rec := range records {
		if rec.HarnessKind == kind && rec.SessionID == sessionID {
			return rec, nil
		}
	}
	return Record{}, fmt.Errorf("%s/%s: %w", kind, sessionID, ErrNotFound)
}

// ByHost returns the record holding that live (pid, startedAt).
func (r *DBRegistry) ByHost(pid int, startedAt int64) (Record, error) {
	if err := r.ensureImported(); err != nil {
		return Record{}, err
	}
	return r.byHostFrom(r.KV, pid, startedAt)
}

func (r *DBRegistry) byHostFrom(kv db.KVTx, pid int, startedAt int64) (Record, error) {
	if pid <= 0 {
		return Record{}, fmt.Errorf("host pid %d: %w", pid, ErrNotFound)
	}
	records, err := r.listFrom(kv)
	if err != nil {
		return Record{}, err
	}
	for _, rec := range records {
		if rec.HostPID == pid && rec.HostStartedAt == startedAt {
			return rec, nil
		}
	}
	return Record{}, fmt.Errorf("host %d@%d: %w", pid, startedAt, ErrNotFound)
}

// List returns every record, sorted by name.
func (r *DBRegistry) List() ([]Record, error) {
	if err := r.ensureImported(); err != nil {
		return nil, err
	}
	return r.listFrom(r.KV)
}

func (r *DBRegistry) listFrom(kv db.KVTx) ([]Record, error) {
	keys, err := kv.KVKeys(mastermindKeyPrefix)
	if err != nil {
		return nil, fmt.Errorf("mastermind: list records: %w", err)
	}

	records := make([]Record, 0, len(keys))
	for _, key := range keys {
		raw, ok, gerr := kv.KVGet(key)
		if gerr != nil {
			return nil, fmt.Errorf("mastermind: read %s: %w", key, gerr)
		}
		if !ok {
			continue
		}
		rec, derr := decodeRecord(key, raw)
		if derr != nil {
			return nil, derr
		}
		records = append(records, rec)
	}

	sort.Slice(records, func(i, j int) bool { return records[i].Name < records[j].Name })
	return records, nil
}

// Create adds a record, refusing a name, session or live host another
// record already holds.
func (r *DBRegistry) Create(rec Record) (Record, error) {
	if err := r.ensureImported(); err != nil {
		return Record{}, err
	}
	var out Record
	err := r.KV.Tx(func(tx db.KVTx) error {
		var e error
		out, e = r.createIn(tx, rec)
		return e
	})
	return out, err
}

func (r *DBRegistry) createIn(kv db.KVTx, rec Record) (Record, error) {
	now := r.now()
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = now
	}
	if rec.SeenAt.IsZero() {
		rec.SeenAt = now
	}
	if err := rec.Validate(); err != nil {
		return Record{}, err
	}

	records, err := r.listFrom(kv)
	if err != nil {
		return Record{}, err
	}
	for _, other := range records {
		switch {
		case other.ID == rec.ID:
			return Record{}, fmt.Errorf("mastermind: id %s already exists: %w", rec.ID, ErrInvalid)
		case other.Name == rec.Name:
			return Record{}, fmt.Errorf("mastermind: name %s: %w", rec.Name, ErrNameTaken)
		case other.HarnessKind == rec.HarnessKind && other.SessionID == rec.SessionID:
			return Record{}, fmt.Errorf("mastermind: session %s/%s: %w", rec.HarnessKind, rec.SessionID, ErrSessionTaken)
		case rec.HostPID > 0 && other.HostPID == rec.HostPID && other.HostStartedAt == rec.HostStartedAt:
			return Record{}, fmt.Errorf("mastermind: host %d@%d: %w", rec.HostPID, rec.HostStartedAt, ErrHostTaken)
		}
	}

	if err := r.writeIn(kv, rec); err != nil {
		return Record{}, err
	}
	return rec, nil
}

// MoveSession points the record at a new session, appending the old one to
// its history with To = now.
func (r *DBRegistry) MoveSession(id, sessionID, transcript string, now time.Time) (Record, error) {
	if err := r.ensureImported(); err != nil {
		return Record{}, err
	}
	var out Record
	err := r.KV.Tx(func(tx db.KVTx) error {
		var e error
		out, e = r.moveSessionIn(tx, id, sessionID, transcript, now)
		return e
	})
	return out, err
}

func (r *DBRegistry) moveSessionIn(kv db.KVTx, id, sessionID, transcript string, now time.Time) (Record, error) {
	rec, err := r.getFrom(kv, id)
	if err != nil {
		return Record{}, err
	}
	if now.IsZero() {
		now = r.now()
	}

	records, err := r.listFrom(kv)
	if err != nil {
		return Record{}, err
	}
	for _, other := range records {
		if other.ID == rec.ID {
			continue
		}
		if other.HarnessKind == rec.HarnessKind && other.SessionID == sessionID {
			return Record{}, fmt.Errorf("mastermind: session %s/%s: %w", rec.HarnessKind, sessionID, ErrSessionTaken)
		}
	}

	if rec.SessionID != sessionID {
		rec.Sessions = appendSession(rec.Sessions, SessionRef{
			SessionID: rec.SessionID,
			From:      rec.sessionStartedAt(),
			To:        now,
		})
		rec.SessionID = sessionID
	}
	// An empty transcript path leaves the stored locator alone.
	if transcript != "" {
		rec.TranscriptLocator = transcript
	}

	if err := rec.Validate(); err != nil {
		return Record{}, err
	}
	if err := r.writeIn(kv, rec); err != nil {
		return Record{}, err
	}
	return rec, nil
}

// SetHost points the record at a harness process, refusing a live host
// another record already holds. pid 0 clears the start time too.
func (r *DBRegistry) SetHost(id string, pid int, startedAt int64) (Record, error) {
	if err := r.ensureImported(); err != nil {
		return Record{}, err
	}
	var out Record
	err := r.KV.Tx(func(tx db.KVTx) error {
		var e error
		out, e = r.setHostIn(tx, id, pid, startedAt)
		return e
	})
	return out, err
}

func (r *DBRegistry) setHostIn(kv db.KVTx, id string, pid int, startedAt int64) (Record, error) {
	rec, err := r.getFrom(kv, id)
	if err != nil {
		return Record{}, err
	}
	if pid < 0 {
		return Record{}, fmt.Errorf("mastermind: host_pid %d must be >= 0: %w", pid, ErrInvalid)
	}
	if pid == 0 {
		startedAt = 0
	}

	if pid > 0 {
		records, err := r.listFrom(kv)
		if err != nil {
			return Record{}, err
		}
		for _, other := range records {
			if other.ID == rec.ID {
				continue
			}
			if other.HostPID == pid && other.HostStartedAt == startedAt {
				return Record{}, fmt.Errorf("mastermind: host %d@%d: %w", pid, startedAt, ErrHostTaken)
			}
		}
	}

	rec.HostPID = pid
	rec.HostStartedAt = startedAt
	if err := rec.Validate(); err != nil {
		return Record{}, err
	}
	if err := r.writeIn(kv, rec); err != nil {
		return Record{}, err
	}
	return rec, nil
}

// Rename gives a record a new name, refusing one another record holds.
func (r *DBRegistry) Rename(id, name string) (Record, error) {
	if err := r.ensureImported(); err != nil {
		return Record{}, err
	}
	var out Record
	err := r.KV.Tx(func(tx db.KVTx) error {
		var e error
		out, e = r.renameIn(tx, id, name)
		return e
	})
	return out, err
}

func (r *DBRegistry) renameIn(kv db.KVTx, id, name string) (Record, error) {
	if err := ValidName(name); err != nil {
		return Record{}, err
	}
	rec, err := r.getFrom(kv, id)
	if err != nil {
		return Record{}, err
	}

	records, err := r.listFrom(kv)
	if err != nil {
		return Record{}, err
	}
	for _, other := range records {
		if other.ID != rec.ID && other.Name == name {
			return Record{}, fmt.Errorf("mastermind: name %s: %w", name, ErrNameTaken)
		}
	}

	rec.Name = name
	if err := r.writeIn(kv, rec); err != nil {
		return Record{}, err
	}
	return rec, nil
}

// Touch moves seen_at forward, at most once per minute. Best effort:
// callers ignore its error.
func (r *DBRegistry) Touch(id string, now time.Time) error {
	if err := r.ensureImported(); err != nil {
		return err
	}
	return r.KV.Tx(func(tx db.KVTx) error { return r.touchIn(tx, id, now) })
}

func (r *DBRegistry) touchIn(kv db.KVTx, id string, now time.Time) error {
	rec, err := r.getFrom(kv, id)
	if err != nil {
		return err
	}
	if now.IsZero() {
		now = r.now()
	}
	if now.Before(rec.SeenAt) || now.Sub(rec.SeenAt) < seenRefreshInterval {
		return nil
	}
	rec.SeenAt = now
	return r.writeIn(kv, rec)
}

// touchForcedIn stamps seen_at with no throttle, unlike Touch.
func (r *DBRegistry) touchForcedIn(kv db.KVTx, id string, now time.Time) (Record, error) {
	rec, err := r.getFrom(kv, id)
	if err != nil {
		return Record{}, err
	}
	if !now.After(rec.SeenAt) {
		return rec, nil
	}
	rec.SeenAt = now
	if err := r.writeIn(kv, rec); err != nil {
		return Record{}, err
	}
	return rec, nil
}

// Forget removes a record; inUse refuses with ErrInUse rather than deleting
// one a live binding names.
func (r *DBRegistry) Forget(id string, inUse func(id string) bool) error {
	if err := r.ensureImported(); err != nil {
		return err
	}
	return r.KV.Tx(func(tx db.KVTx) error { return r.forgetIn(tx, id, inUse) })
}

func (r *DBRegistry) forgetIn(kv db.KVTx, id string, inUse func(id string) bool) error {
	rec, err := r.getFrom(kv, id)
	if err != nil {
		return err
	}
	if inUse != nil && inUse(rec.ID) {
		return fmt.Errorf("mastermind %s (%s) is named by a binding that is not done: %w", rec.Name, rec.ID, ErrInUse)
	}
	if err := kv.KVDelete(registryKey(rec.ID)); err != nil {
		return fmt.Errorf("mastermind: remove %s: %w", rec.ID, err)
	}
	return nil
}

// writeIn puts one record's whole document in its row.
func (r *DBRegistry) writeIn(kv db.KVTx, rec Record) error {
	// A record written by a newer relevo is read-only: its rewrite would
	// erase fields this relevo does not know.
	if rec.Format > MasterMindFormat {
		return &store.ErrNewerFormat{Kind: "mastermind record", Name: rec.Name, Have: rec.Format, Know: MasterMindFormat}
	}
	rec.Format = storedFormat(MasterMindFormat)

	raw, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("mastermind: marshal %s: %w", rec.ID, err)
	}
	if err := kv.KVPut(registryKey(rec.ID), raw); err != nil {
		return fmt.Errorf("mastermind: write %s: %w", rec.ID, err)
	}
	return nil
}
