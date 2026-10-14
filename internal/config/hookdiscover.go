package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// DiscoverHookFiles is the set of scripts the stored argv rows name, read off
// this machine's disk. An executable no row names is never read: naming a
// script in a hook is the person's consent to publish its bytes, and nothing
// else under the config root is.
// The second map it returns is the hash cache that describes how the read went.
//
// The capture is cheap because it stats first. A script whose size,
// modification time and executable bit are what the cache last recorded is
// taken from the document rather than read again, so an unchanged hook costs one stat per file. Anything
// the cache cannot vouch for is read once, and a file that is not a regular file
// is left out rather than followed.
func DiscoverHookFiles(root string, cache map[string]fileHash, known map[string]HookFile, events map[string][][]string) (map[string]HookFile, map[string]fileHash, error) {
	out := map[string]HookFile{}
	hashes := map[string]fileHash{}
	if root == "" {
		return out, hashes, nil
	}
	for _, rel := range argvHookPaths(root, events) {
		if _, done := out[rel]; done || validateHookFilePath(rel) != nil {
			continue
		}
		if err := keepHookScript(root, rel, cache, known, out, hashes); err != nil {
			return nil, nil, err
		}
	}
	return out, hashes, nil
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
//
// A path that is gone records nothing rather than failing the walk. A person
// who deletes a script a hook names has made an edit, and it is the capture's
// job to carry it: refusing here would fail the export and the capture both,
// over a deletion the document is supposed to travel. A genuine read failure is
// still an error, because it says nothing about what the file holds.
func readHookScript(root, rel string, cache map[string]fileHash, known map[string]HookFile) (*HookFile, fileHash, error) {
	full := filepath.Join(root, filepath.FromSlash(rel))
	info, err := os.Lstat(full)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fileHash{}, nil
		}
		return nil, fileHash{}, err
	}
	if !info.Mode().IsRegular() {
		return nil, fileHash{}, nil
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
