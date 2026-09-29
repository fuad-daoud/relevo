package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/usage"
)

type ImportResult struct {
	Imported []string // files and "hooks" consumed
	Removed  []string // paths deleted after the commit
}

type pendingSection struct {
	sec  Section
	path string
	data []byte
}

type pendingImport struct {
	sections      []pendingSection
	clientKey     []byte
	haveClientKey bool
	typesafe      string
	haveTypesafe  bool
	hooks         HooksMap
	importHooks   bool
}

// ImportFiles is the one-time migration and the way config is dropped in
// (provisioning, tests). A present file is validated and stored with its raw
// bytes, and removed only after the commit, so a crash leaves the files and
// the next verb re-imports the same bytes idempotently.
func (s *Store) ImportFiles(configDir string, now time.Time) (ImportResult, error) {
	// Every revision this import writes is stamped with the time the caller
	// gave, and labelled "import" unless the caller labelled it.
	s = s.WithClock(func() time.Time { return now })
	if s.source == "" {
		s.source = "import"
	}

	sections, err := readConfigSections(configDir)
	if err != nil {
		return ImportResult{}, err
	}

	clientKeyPath := filepath.Join(configDir, clientKeyFile)
	clientKey, haveClientKey, err := readClientKey(clientKeyPath)
	if err != nil {
		return ImportResult{}, err
	}

	typesafePath := filepath.Join(configDir, typesafeKeyFile)
	typesafe, haveTypesafe, err := readTypesafeKey(typesafePath)
	if err != nil {
		return ImportResult{}, err
	}

	hooksDir := filepath.Join(configDir, "hooks")
	hooksStored, err := s.Has(Hooks)
	if err != nil {
		return ImportResult{}, err
	}
	importHooks := dirExists(hooksDir) && !hooksStored

	if len(sections) == 0 && !haveClientKey && !haveTypesafe && !importHooks {
		return ImportResult{}, nil
	}

	pending := pendingImport{
		sections:      sections,
		clientKey:     clientKey,
		haveClientKey: haveClientKey,
		typesafe:      typesafe,
		haveTypesafe:  haveTypesafe,
		importHooks:   importHooks,
	}
	if importHooks {
		pending.hooks, err = readHooksDir(hooksDir)
		if err != nil {
			return ImportResult{}, err
		}
	}

	if err := s.commitImport(pending, now); err != nil {
		return ImportResult{}, err
	}
	return s.afterImport(configDir, pending, clientKeyPath, typesafePath)
}

func readConfigSections(configDir string) ([]pendingSection, error) {
	var sections []pendingSection
	for _, sec := range Sections {
		name := FileName(sec)
		if name == "" {
			continue
		}
		path := filepath.Join(configDir, name)
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if _, err := Validate(sec, data); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		sections = append(sections, pendingSection{sec: sec, path: path, data: data})
	}
	return sections, nil
}

func (s *Store) commitImport(p pendingImport, now time.Time) error {
	return s.db.Tx(func(t *db.Tx) error {
		before, err := readSnapshot(t)
		if err != nil {
			return err
		}
		for _, sf := range p.sections {
			if err := t.ConfigPut(string(sf.sec), sf.data, now); err != nil {
				return err
			}
			if err := t.ConfigImportRecord(filepath.Base(sf.path), sf.path, sf.data, now); err != nil {
				return err
			}
		}
		var extra []Change
		if p.haveClientKey {
			if err := t.SecretPut(SecretClientKey, p.clientKey, now); err != nil {
				return err
			}
			extra = append(extra, Change{Path: "secret." + SecretClientKey, Op: "set"})
		}
		if p.haveTypesafe {
			if err := t.SecretPut(SecretTypesafe, []byte(p.typesafe), now); err != nil {
				return err
			}
			extra = append(extra, Change{Path: "secret." + SecretTypesafe, Op: "set"})
		}
		if p.importHooks {
			body, err := json.Marshal(p.hooks)
			if err != nil {
				return err
			}
			if err := t.ConfigPut(string(Hooks), body, now); err != nil {
				return err
			}
		}
		return s.record(t, before, extra)
	})
}

func (s *Store) afterImport(configDir string, p pendingImport, clientKeyPath, typesafePath string) (ImportResult, error) {
	var res ImportResult
	remove := func(path string) {
		err := os.Remove(path)
		if err == nil {
			res.Removed = append(res.Removed, filepath.Base(path))
			return
		}
		if !errors.Is(err, os.ErrNotExist) {
			slog.Warn("config import: remove failed", "path", path, "err", err)
		}
	}

	for _, sf := range p.sections {
		res.Imported = append(res.Imported, filepath.Base(sf.path))
		remove(sf.path)
	}
	if p.haveClientKey {
		res.Imported = append(res.Imported, clientKeyFile)
		remove(clientKeyPath)
	}
	if p.haveTypesafe {
		res.Imported = append(res.Imported, typesafeKeyFile)
		remove(typesafePath)
	}
	if p.importHooks {
		res.Imported = append(res.Imported, "hooks")
	}

	// client.pub is dead once the client key is in the database: remove it
	// when this run imported the key, or when one is already stored.
	if _, keyInDB, err := s.db.SecretGet(SecretClientKey); err != nil {
		return ImportResult{}, err
	} else if p.haveClientKey || keyInDB {
		remove(filepath.Join(configDir, clientPubFile))
	}

	// aliases.json is never read or rewritten.
	remove(filepath.Join(configDir, aliasesFile))

	// The hooks scripts are never removed, so a config dir holding them stays.
	// Remove the directory itself only when nothing is left in it.
	_ = os.Remove(configDir)
	return res, nil
}

// readClientKey validates the client key file; an absent file is ok with no
// data.
func readClientKey(path string) (data []byte, ok bool, err error) {
	raw, rerr := os.ReadFile(path)
	if errors.Is(rerr, os.ErrNotExist) {
		return nil, false, nil
	}
	if rerr != nil {
		return nil, false, fmt.Errorf("%s: %w", path, rerr)
	}
	if _, perr := remote.ParsePrivate(raw); perr != nil {
		return nil, false, fmt.Errorf("%s: %w", path, perr)
	}
	return raw, true, nil
}

// readTypesafeKey rejects a present but blank key file: importing it would
// store no key at all.
func readTypesafeKey(path string) (string, bool, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("%s: %w", path, err)
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return "", false, fmt.Errorf("%s: %w", path, errors.New("empty key"))
	}
	return trimmed, true, nil
}

// LoadFiles is the read-only file load daemon --preflight and --check use:
// today's file reads, with no import and no database.
func LoadFiles(configDir string) (Loaded, error) {
	var L Loaded

	set, candWarnings, err := candidate.LoadWithWarnings(filepath.Join(configDir, FileName(Candidates)))
	if err != nil {
		return Loaded{}, err
	}
	L.Candidates = set
	L.Warnings = append(L.Warnings, candWarnings...)

	pol, polWarnings, err := policy.LoadWithWarnings(filepath.Join(configDir, FileName(Policy)))
	if err != nil {
		return Loaded{}, err
	}
	L.Policy = pol
	L.Warnings = append(L.Warnings, polWarnings...)

	rf, roleWarnings, err := roles.LoadWithWarnings(filepath.Join(configDir, FileName(Roles)))
	if err != nil {
		return Loaded{}, err
	}
	L.RolesFile = rf
	L.Warnings = append(L.Warnings, roleWarnings...)

	L.Prices, err = usage.LoadPrices(filepath.Join(configDir, FileName(Prices)))
	if err != nil {
		return Loaded{}, err
	}

	L.Servers = remote.Servers{}
	if raw, err := os.ReadFile(filepath.Join(configDir, FileName(Servers))); err == nil {
		L.Servers, err = remote.ParseServers(raw)
		if err != nil {
			return Loaded{}, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Loaded{}, fmt.Errorf("%s: %w", filepath.Join(configDir, FileName(Servers)), err)
	}

	L.Hooks, err = readHooksDir(filepath.Join(configDir, "hooks"))
	if err != nil {
		return Loaded{}, err
	}

	if raw, err := os.ReadFile(filepath.Join(configDir, clientKeyFile)); err == nil {
		if _, perr := remote.ParsePrivate(raw); perr != nil {
			return Loaded{}, fmt.Errorf("%s: %w", filepath.Join(configDir, clientKeyFile), perr)
		}
		L.ClientKey = raw
	} else if !errors.Is(err, os.ErrNotExist) {
		return Loaded{}, fmt.Errorf("%s: %w", filepath.Join(configDir, clientKeyFile), err)
	}

	if raw, err := os.ReadFile(filepath.Join(configDir, typesafeKeyFile)); err == nil {
		L.Typesafe = strings.TrimSpace(string(raw))
	} else if !errors.Is(err, os.ErrNotExist) {
		return Loaded{}, fmt.Errorf("%s: %w", filepath.Join(configDir, typesafeKeyFile), err)
	}

	reg, err := roles.Build(L.RolesFile, L.Candidates, L.Policy)
	if err != nil {
		return Loaded{}, err
	}
	L.Registry = reg

	return L, nil
}

// readHooksDir builds the hooks section from <hooksDir>/<event>.d, taking the
// executable files in each directory in file-name order.
func readHooksDir(hooksDir string) (HooksMap, error) {
	hooks := HooksMap{}
	entries, err := os.ReadDir(hooksDir)
	if errors.Is(err, os.ErrNotExist) {
		return hooks, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", hooksDir, err)
	}

	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasSuffix(entry.Name(), ".d") {
			continue
		}
		event := strings.TrimSuffix(entry.Name(), ".d")
		if event == "" {
			continue
		}
		sub := filepath.Join(hooksDir, entry.Name())
		files, err := os.ReadDir(sub)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", sub, err)
		}
		var argv [][]string
		for _, f := range files {
			if f.IsDir() {
				continue
			}
			info, err := f.Info()
			if err != nil {
				continue
			}
			if info.Mode()&0o111 == 0 {
				continue
			}
			argv = append(argv, []string{filepath.Join(sub, f.Name())})
		}
		if len(argv) > 0 {
			hooks[event] = argv
		}
	}
	return hooks, nil
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
