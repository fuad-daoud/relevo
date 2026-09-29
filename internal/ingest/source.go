// Package ingest reads a binding -- live under ~/.local/state/relevo/<name>/,
// in the store's database, or archived as a record -- and upserts every fact it
// holds into internal/db.
package ingest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// Source is one binding, live or archived: it decodes bind.json, lists and opens
// member files by basename, and reports its own origin.
type Source interface {
	Name() string
	// Bind decodes bind.json. ErrSource when it is missing or invalid.
	Bind() (store.Binding, error)
	// Open returns member's content and size; os.ErrNotExist when absent.
	Open(member string) (io.ReadCloser, int64, error)
	List() ([]string, error)
	// Origin reports "live" and the directory, or "archive" and an empty path.
	Origin() (kind, path string)
}

// archivedAtter is a Source that knows when it was archived; the mirror's binding
// row carries that stamp.
type archivedAtter interface {
	ArchivedAt() (time.Time, bool)
}

type dirSource struct {
	dir string
}

// DirSource returns a Source over a live binding directory.
func DirSource(dir string) Source { return dirSource{dir: filepath.Clean(dir)} }

func (d dirSource) Name() string { return filepath.Base(d.dir) }

func (d dirSource) Bind() (store.Binding, error) {
	data, err := os.ReadFile(filepath.Join(d.dir, "bind.json"))
	if err != nil {
		return store.Binding{}, fmt.Errorf("%w: %s: %w", ErrSource, d.dir, err)
	}
	var b store.Binding
	if err := json.Unmarshal(data, &b); err != nil {
		return store.Binding{}, fmt.Errorf("%w: %s: decode bind.json: %w", ErrSource, d.dir, err)
	}
	return b, nil
}

func (d dirSource) Open(member string) (io.ReadCloser, int64, error) {
	f, err := os.Open(filepath.Join(d.dir, member))
	if err != nil {
		return nil, 0, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, err
	}
	return f, info.Size(), nil
}

func (d dirSource) List() ([]string, error) {
	entries, err := os.ReadDir(d.dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}

func (d dirSource) Origin() (string, string) { return "live", d.dir }

type storeSource struct {
	st   *store.Store
	name string
}

// StoreSource returns a Source over a live binding held in the store's database:
// bind.json and log.jsonl come from Load and ReadLog, and every other member from
// Store.ReadFile -- disk while the round is open, the sealed round_file row once
// it is not.
func StoreSource(st *store.Store, name string) Source {
	return storeSource{st: st, name: name}
}

func (s storeSource) Name() string { return s.name }

func (s storeSource) Bind() (store.Binding, error) {
	b, err := s.st.Load(s.name)
	if err != nil {
		return store.Binding{}, fmt.Errorf("%w: %s: %w", ErrSource, s.st.Dir(s.name), err)
	}
	return b, nil
}

func (s storeSource) Open(member string) (io.ReadCloser, int64, error) {
	switch member {
	case "bind.json":
		b, err := s.st.Load(s.name)
		if err != nil {
			return nil, 0, err
		}
		data, err := json.MarshalIndent(b, "", "  ")
		if err != nil {
			return nil, 0, err
		}
		return io.NopCloser(bytes.NewReader(data)), int64(len(data)), nil
	case "log.jsonl":
		entries, err := s.st.ReadLog(s.name)
		if err != nil {
			return nil, 0, err
		}
		var buf bytes.Buffer
		for _, e := range entries {
			line, err := json.Marshal(e)
			if err != nil {
				return nil, 0, err
			}
			buf.Write(line)
			buf.WriteByte('\n')
		}
		data := buf.Bytes()
		return io.NopCloser(bytes.NewReader(data)), int64(len(data)), nil
	default:
		if !roundMember(member) {
			return dirSource{dir: s.st.Dir(s.name)}.Open(member)
		}
		path := filepath.Join(s.st.Dir(s.name), member)
		data, err := s.st.ReadFile(path)
		if err != nil {
			return nil, 0, err
		}
		return io.NopCloser(bytes.NewReader(data)), int64(len(data)), nil
	}
}

func (s storeSource) List() ([]string, error) {
	names, err := s.st.RoundFiles(s.name)
	if err != nil {
		return nil, err
	}
	names = append(names, "bind.json", "log.jsonl")
	sort.Strings(names)
	return names, nil
}

func (s storeSource) Origin() (string, string) { return "live", s.st.Dir(s.name) }

type archivedSource struct {
	st       *store.Store
	recordID string
}

// ArchivedSource returns a Source over an archived record held in the store's
// database: bind.json is the record re-indented, log.jsonl is ArchivedLog's
// entries, and every NNN-* member is read through Store.ArchivedFile.
func ArchivedSource(st *store.Store, recordID string) Source {
	return archivedSource{st: st, recordID: recordID}
}

func (s archivedSource) Name() string {
	b, ok, err := s.st.ArchivedRecord(s.recordID)
	if err != nil || !ok {
		return ""
	}
	return b.Name
}

func (s archivedSource) Bind() (store.Binding, error) {
	b, ok, err := s.st.ArchivedRecord(s.recordID)
	if err != nil {
		return store.Binding{}, fmt.Errorf("%w: record %s: %w", ErrSource, s.recordID, err)
	}
	if !ok {
		return store.Binding{}, fmt.Errorf("%w: record %s: not found", ErrSource, s.recordID)
	}
	return b, nil
}

func (s archivedSource) Open(member string) (io.ReadCloser, int64, error) {
	switch member {
	case "bind.json":
		b, err := s.Bind()
		if err != nil {
			return nil, 0, err
		}
		data, err := json.MarshalIndent(b, "", "  ")
		if err != nil {
			return nil, 0, err
		}
		return io.NopCloser(bytes.NewReader(data)), int64(len(data)), nil
	case "log.jsonl":
		entries, err := s.st.ArchivedLog(s.recordID)
		if err != nil {
			return nil, 0, err
		}
		var buf bytes.Buffer
		for _, e := range entries {
			line, err := json.Marshal(e)
			if err != nil {
				return nil, 0, err
			}
			buf.Write(line)
			buf.WriteByte('\n')
		}
		data := buf.Bytes()
		return io.NopCloser(bytes.NewReader(data)), int64(len(data)), nil
	default:
		if !roundMember(member) {
			return nil, 0, os.ErrNotExist
		}
		body, ok, err := s.st.ArchivedFile(s.recordID, member)
		if err != nil {
			return nil, 0, err
		}
		if !ok {
			return nil, 0, os.ErrNotExist
		}
		return io.NopCloser(bytes.NewReader(body)), int64(len(body)), nil
	}
}

func (s archivedSource) List() ([]string, error) {
	names, err := s.st.ArchivedFiles(s.recordID)
	if err != nil {
		return nil, err
	}
	names = append(names, "bind.json", "log.jsonl")
	sort.Strings(names)
	return names, nil
}

// Origin reports the kind the mirror records; the path is empty, since an
// archived record has no directory behind it.
func (s archivedSource) Origin() (string, string) { return "archive", "" }

func (s archivedSource) ArchivedAt() (time.Time, bool) {
	return s.st.ArchivedAtOf(s.recordID)
}

// roundMember reports whether member is a round file's basename: the NNN- prefix
// every file relevo writes for a round carries. Only those can be sealed.
func roundMember(member string) bool {
	if len(member) < 4 || member[3] != '-' {
		return false
	}
	for i := 0; i < 3; i++ {
		if member[i] < '0' || member[i] > '9' {
			return false
		}
	}
	return true
}
