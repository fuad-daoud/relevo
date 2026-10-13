package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// DiscoverHookFiles is the set of scripts on disk under this machine's hooks
// directory: the executable files of every `<event>.d` directory, which is
// what readHooksDir would import, plus every script the stored argv rows name.
// The second map it returns is the hash cache that describes how the walk read
// them.
//
// The capture is cheap because the walk stats first. A script whose size,
// modification time and executable bit are what the cache last recorded is
// taken from the document rather than read again, so an unchanged hook costs
// one stat per file. Anything the cache cannot vouch for is read once, and a
// file that is not a regular file is left out rather than followed.
func DiscoverHookFiles(root string, cache map[string]fileHash, known map[string]HookFile, events map[string][][]string) (map[string]HookFile, map[string]fileHash, error) {
	out := map[string]HookFile{}
	hashes := map[string]fileHash{}
	if root == "" {
		return out, hashes, nil
	}
	keep := map[string]bool{}
	if err := walkHookEventDirs(root, func(rel string) error {
		keep[rel] = true
		return keepHookScript(root, rel, cache, known, out, hashes)
	}); err != nil {
		return nil, nil, err
	}
	// An argv row naming a script that is not executable still carries it: the
	// row says the script runs, and the map says what it contains.
	for _, rel := range argvHookPaths(root, events) {
		if keep[rel] {
			continue
		}
		if _, named := known[rel]; !named {
			continue
		}
		keep[rel] = true
		if err := keepHookScript(root, rel, cache, known, out, hashes); err != nil {
			return nil, nil, err
		}
	}
	return out, hashes, nil
}

// walkHookEventDirs calls fn once per executable script under every
// `<config root>/hooks/<event>.d` directory, in name order. An event whose name
// is not one plain component is left out rather than walked.
func walkHookEventDirs(root string, fn func(rel string) error) error {
	hooksDir := filepath.Join(root, hooksDirName)
	entries, err := os.ReadDir(hooksDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasSuffix(entry.Name(), hooksSuffix) {
			continue
		}
		event := strings.TrimSuffix(entry.Name(), hooksSuffix)
		if event == "" || safeComponent(event) != nil {
			continue
		}
		sub := filepath.Join(hooksDir, entry.Name())
		files, err := os.ReadDir(sub)
		if err != nil {
			return err
		}
		for _, f := range files {
			info, err := f.Info()
			if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
				continue
			}
			if err := fn(HookPathOf(event, f.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// argvHookPaths is every root-relative script the stored argv rows name.
func argvHookPaths(root string, events map[string][][]string) []string {
	var out []string
	for _, event := range sortedEvents(events) {
		for _, row := range events[event] {
			if len(row) == 0 {
				continue
			}
			rel, local := RelativizeHookArgv(root, row[0])
			if local {
				continue
			}
			out = append(out, rel)
		}
	}
	return out
}

// keepHookScript records one script and its cache entry, or nothing when the
// path is absent, is not a regular file, or is over the per-file cap.
func keepHookScript(root, rel string, cache map[string]fileHash, known map[string]HookFile, out map[string]HookFile, hashes map[string]fileHash) error {
	file, entry, err := readHookScript(root, rel, cache, known)
	if err != nil || file == nil {
		return err
	}
	out[rel] = *file
	hashes[rel] = entry
	return nil
}

// readHookScript reads one script through the cache: a cached entry the file
// still matches costs no read at all, and the document's copy supplies the
// bytes. Anything else is read, capped and hashed.
func readHookScript(root, rel string, cache map[string]fileHash, known map[string]HookFile) (*HookFile, fileHash, error) {
	full := filepath.Join(root, filepath.FromSlash(rel))
	info, err := os.Lstat(full)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fileHash{}, err
	}
	exec := info.Mode()&0o111 != 0
	mod := info.ModTime().UnixNano()
	if cached, ok := cache[rel]; ok && cached.matches(info) {
		if file, held := known[rel]; held {
			out := file
			out.Exec = exec
			return &out, cached, nil
		}
	}
	data, err := readLimited(full, MaxHookFileBytes)
	if err != nil {
		return nil, fileHash{}, err
	}
	file := HookFile{Contents: data, Exec: exec}
	return &file, fileHash{Hash: file.Hash(), Size: info.Size(), Exec: exec, Mod: mod}, nil
}

// readHookFileAt reads one root-relative script, reporting nil when it is
// absent or is not a regular file.
func readHookFileAt(root, rel string) (*HookFile, error) {
	full := filepath.Join(root, filepath.FromSlash(rel))
	info, err := os.Lstat(full)
	if err != nil || !info.Mode().IsRegular() {
		return nil, err
	}
	data, err := readLimited(full, MaxHookFileBytes)
	if err != nil {
		return nil, err
	}
	return &HookFile{Contents: data, Exec: info.Mode()&0o111 != 0}, nil
}
