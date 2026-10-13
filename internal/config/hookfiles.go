package config

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// The hooks section carries the scripts themselves and not only the argv rows
// that name them, so a machine that receives a hook receives the hook. The
// bodies are shaped by three rules and nothing else:
//
//   - the shared half holds the scripts under this machine's config root, named
//     root-relative, so a peer that stores them under a different root still
//     applies them to the right directory;
//   - an argv row naming anything outside the config root names a file on this
//     machine, so it never travels: it is split out into the machine field
//     `local` before the write leaves this machine, and the stored shared body
//     never carries it;
//   - the file map and the argv rows are kept apart on purpose. argv says which
//     script runs; the file map says what it contains. A person editing a
//     script changes the second without touching the first, which is what lets
//     the capture export the edit on its own.

// The reserved members of the hooks value. Anything else in the object is an
// event name, so a body written before this shape existed still reads: it is
// the argv map itself, with no wrapper.
const (
	hooksEventsMember = "events"
	hooksFilesMember  = "files"
	hooksLocalMember  = "local"
)

// hooksDirName is the directory under the config root the hook scripts live
// in. The directory is computed from the event name, never read out of the
// entry, so an entry cannot steer a write anywhere else.
const hooksDirName = "hooks"

// hooksSuffix is the directory suffix readHooksDir treats as one event's
// script directory.
const hooksSuffix = ".d"

// Caps. One file over the first is refused on its own, whatever the total; a
// hooks value over the second is refused whole, because a peer would have to
// carry it.
const (
	// MaxHookFileBytes is one embedded script.
	MaxHookFileBytes = 256 << 10
	// MaxHooksValueBytes is one hooks item, every script and argv row in it.
	MaxHooksValueBytes = 2 << 20
	// maxHookComponent is the longest event or file name accepted. Both name
	// one directory level, and no filesystem reached here has a longer limit
	// than this.
	maxHookComponent = 128
)

// ErrHookPath marks a refusal of a name or path: what the entry asked to be
// written is refused, and nothing of the store has failed.
var ErrHookPath = errors.New("hooks: unsafe path")

// errOversize is the cap refusal's own cause, so a caller can tell a value
// that is too big from a value that is malformed.
var errOversize = errors.New("over the cap")

// HookFile is one hook script as the document carries it.
type HookFile struct {
	Contents []byte
	Exec     bool
}

// Hash is the script's content digest: the identity the hash cache keeps and
// the applier compares against before it overwrites anything.
func (f HookFile) Hash() string {
	sum := sha256.Sum256(f.Contents)
	return hex.EncodeToString(sum[:])
}

// hookFileValue is one script on the wire. The contents are base64 because a
// script is a byte string and JSON text cannot carry one that is not valid
// UTF-8 without silently changing it.
type hookFileValue struct {
	Contents string `json:"contents"`
	Exec     bool   `json:"exec"`
}

// hookValue is the hooks section's shape.
type hookValue struct {
	Events map[string][][]string    `json:"events,omitempty"`
	Files  map[string]hookFileValue `json:"files,omitempty"`
	Local  map[string][][]string    `json:"local,omitempty"`
}

// HookDoc is the parsed hooks section: the shared half (events and files), the
// machine half (local), and the config root every shared name is relative to.
type HookDoc struct {
	Events map[string][][]string
	Files  map[string]HookFile
	Local  map[string][][]string
	Root   string
}

func emptyHookDoc() HookDoc {
	return HookDoc{
		Events: map[string][][]string{},
		Files:  map[string]HookFile{},
		Local:  map[string][][]string{},
	}
}

// ParseHookDoc reads a hooks section body, accepting the shape this package
// writes and the bare argv map a database written before it carried. The two
// are told apart by the reserved members: a body that has one of them is the
// new shape, and a body with none is every member an event.
func ParseHookDoc(body json.RawMessage) (HookDoc, error) {
	doc := emptyHookDoc()
	if len(body) == 0 {
		return doc, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil || obj == nil {
		return doc, fmt.Errorf("%s: not an object", Hooks)
	}
	if !reservedHooksShape(obj) {
		events, err := parseHookEvents(obj)
		if err != nil {
			return HookDoc{}, err
		}
		doc.Events = events
		return doc, nil
	}
	if err := decodeHookMember(obj, hooksEventsMember, &doc.Events); err != nil {
		return HookDoc{}, err
	}
	if err := decodeHookMember(obj, hooksFilesMember, &doc.Files); err != nil {
		return HookDoc{}, err
	}
	if err := decodeHookMember(obj, hooksLocalMember, &doc.Local); err != nil {
		return HookDoc{}, err
	}
	if doc.Events == nil {
		doc.Events = map[string][][]string{}
	}
	if doc.Files == nil {
		doc.Files = map[string]HookFile{}
	}
	if doc.Local == nil {
		doc.Local = map[string][][]string{}
	}
	return doc, nil
}

// reservedHooksShape reports whether body carries the wrapped shape. A member
// that is not reserved is an event, so the check is for the reserved members
// alone and a wrapped body with none of them (an empty object) reads the same
// way either round.
func reservedHooksShape(obj map[string]json.RawMessage) bool {
	for _, member := range []string{hooksEventsMember, hooksFilesMember, hooksLocalMember} {
		if _, ok := obj[member]; ok {
			return true
		}
	}
	return false
}

func decodeHookMember(obj map[string]json.RawMessage, member string, out any) error {
	raw, ok := obj[member]
	if !ok {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s: %s: %w", Hooks, member, err)
	}
	return nil
}

func parseHookEvents(obj map[string]json.RawMessage) (map[string][][]string, error) {
	events := map[string][][]string{}
	names := make([]string, 0, len(obj))
	for name := range obj {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		var rows [][]string
		if err := json.Unmarshal(obj[name], &rows); err != nil {
			return nil, fmt.Errorf("%s: %s: %w", Hooks, name, err)
		}
		events[name] = rows
	}
	return events, nil
}

// EncodeHookDoc is the hooks section as the config doc stores it. The machine
// half is not written: it is the local layer's row, and writing it here would
// put this machine's directories into the shared body the exporter sends.
func EncodeHookDoc(doc HookDoc) (json.RawMessage, error) {
	value := hookValue{Events: doc.Events, Files: map[string]hookFileValue{}}
	for name, file := range doc.Files {
		value.Files[name] = hookFileValue{
			Contents: base64.StdEncoding.EncodeToString(file.Contents),
			Exec:     file.Exec,
		}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(encoded) > MaxHooksValueBytes {
		return nil, fmt.Errorf("%s: value is %d bytes, over the %d cap: %w",
			Hooks, len(encoded), MaxHooksValueBytes, ErrInvalidValue)
	}
	return encoded, nil
}

// EncodeHookLocal is the machine half as the local layer stores it: one patch
// under the hooks key whose only member is `local`.
func EncodeHookLocal(doc HookDoc) (json.RawMessage, error) {
	if len(doc.Local) == 0 {
		return nil, nil
	}
	patch, err := json.Marshal(doc.Local)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]json.RawMessage{hooksLocalMember: patch})
}

// HookPathOf is the root-relative name one script of one event is carried
// under: `hooks/<event>.d/<file>`. The directory is computed from the two
// names, so an entry cannot name a directory of its own choosing.
func HookPathOf(event, file string) string {
	return path.Join(hooksDirName, event+hooksSuffix, file)
}

// HookParts is one script's names read back off its root-relative key.
func HookParts(rel string) (event, file string, err error) {
	if path.IsAbs(rel) {
		return "", "", fmt.Errorf("hooks: %q is not a root-relative script path: %w", rel, ErrHookPath)
	}
	clean := path.Clean(rel)
	eventDir, base := path.Split(clean)
	eventDir = strings.TrimSuffix(eventDir, "/")
	if path.Dir(eventDir) != hooksDirName {
		return "", "", fmt.Errorf("hooks: %q is not under %s: %w",
			rel, path.Join(hooksDirName, "<event>"+hooksSuffix), ErrHookPath)
	}
	event, ok := strings.CutSuffix(path.Base(eventDir), hooksSuffix)
	if !ok || event == "" || base == "" {
		return "", "", fmt.Errorf("hooks: %q is not a hook script path: %w", rel, ErrHookPath)
	}
	return event, base, nil
}

// ValidateHookDoc refuses everything decision 7 and the path rules name, one
// rule per refusal: an event or file name that is not one plain component, a
// path that is not the one the directory rule computes, a script over the
// per-file cap, a value over the whole-item cap, and a shared argv row naming
// something outside the config root.
func ValidateHookDoc(doc HookDoc) error {
	if doc.Root == "" {
		// Nothing on this machine to be relative to: the shared half cannot be
		// checked against a root, so it is not checked at all. A store with no
		// root is the pure-test store, where nothing is written to disk.
		return nil
	}
	for _, event := range sortedEvents(doc.Events) {
		if err := safeComponent(event); err != nil {
			return err
		}
		for _, row := range doc.Events[event] {
			if len(row) == 0 {
				return fmt.Errorf("hooks: %s: empty argv: %w", event, ErrInvalidValue)
			}
			if err := validateSharedArgv(doc.Root, event, row[0]); err != nil {
				return err
			}
		}
	}
	for _, event := range sortedEvents(doc.Local) {
		if err := safeComponent(event); err != nil {
			return err
		}
		for _, row := range doc.Local[event] {
			if len(row) == 0 {
				return fmt.Errorf("hooks: %s: empty argv: %w", event, ErrInvalidValue)
			}
			if filepath.IsAbs(row[0]) {
				continue
			}
			return fmt.Errorf("hooks: %s: argv %q is machine-local and must be absolute: %w",
				event, row[0], ErrHookPath)
		}
	}
	for _, rel := range sortedFiles(doc.Files) {
		if err := validateHookFilePath(rel); err != nil {
			return err
		}
		if len(doc.Files[rel].Contents) > MaxHookFileBytes {
			return fmt.Errorf("hooks: %s: %d bytes, over the %d cap: %w",
				rel, len(doc.Files[rel].Contents), MaxHookFileBytes, ErrInvalidValue)
		}
	}
	encoded, err := json.Marshal(hookValue{Events: doc.Events, Files: hookFileValues(doc.Files)})
	if err != nil {
		return err
	}
	if len(encoded) > MaxHooksValueBytes {
		return fmt.Errorf("%s: value is %d bytes, over the %d cap: %w",
			Hooks, len(encoded), MaxHooksValueBytes, ErrInvalidValue)
	}
	return nil
}

// validateHookFilePath checks one embedded script's root-relative key against
// the directory rule: it has to be the path the event and file names compute.
func validateHookFilePath(rel string) error {
	if path.IsAbs(rel) || strings.Contains(rel, `\`) || strings.ContainsRune(rel, 0) {
		return fmt.Errorf("hooks: %q is not a root-relative script path: %w", rel, ErrHookPath)
	}
	event, base, err := HookParts(rel)
	if err != nil {
		return fmt.Errorf("hooks: %q is not under %s: %w",
			rel, path.Join(hooksDirName, "<event>"+hooksSuffix), ErrHookPath)
	}
	if err := safeComponent(event); err != nil {
		return err
	}
	return safeComponent(base)
}

// validateSharedArgv checks one shared argv row: its script has to be a
// root-relative path under this machine's hooks directory, and it has to be
// the path its own event name computes.
func validateSharedArgv(root, event, script string) error {
	if filepath.IsAbs(script) {
		return fmt.Errorf("hooks: %s: argv %q names a file outside the config root and stays machine-local: %w",
			event, script, ErrHookPath)
	}
	if err := safeComponent(event); err != nil {
		return err
	}
	_, base, err := HookParts(script)
	if err != nil {
		return fmt.Errorf("hooks: %s: argv %q is not under its own event directory: %w", event, script, ErrHookPath)
	}
	if got, want := path.Dir(path.Clean(script)), path.Join(hooksDirName, event+hooksSuffix); got != want {
		return fmt.Errorf("hooks: %s: argv %q is not under its own event directory: %w", event, script, ErrHookPath)
	}
	_ = root
	_ = base
	return nil
}

// SafeHookComponent refuses a name that is not one plain path component. It is
// the exported form, so the disk applier and the document agree on the rule
// instead of each keeping its own.
func SafeHookComponent(name string) error { return safeComponent(name) }

// safeComponent refuses a name that is not one plain path component, saying
// hooks because it is the hooks' rule; the agents' names take the same rule
// through safeAgentComponent, which says agents.
func safeComponent(name string) error { return safeNamedComponent("hooks", name, ErrHookPath) }

// safeNamedComponent is one plain-component rule with the section that applies
// it. The refusals are separate because they are separate mistakes: a slash, a
// backslash, a dot name, a NUL, an over-long name.
func safeNamedComponent(section, name string, cause error) error {
	switch {
	case name == "":
		return fmt.Errorf("%s: empty name: %w", section, cause)
	case name == "." || name == "..":
		return fmt.Errorf("%s: %q is a directory name, not a file name: %w", section, name, cause)
	case strings.ContainsRune(name, 0):
		return fmt.Errorf("%s: name holds a NUL: %w", section, cause)
	case strings.ContainsRune(name, '/'):
		return fmt.Errorf("%s: %q holds a slash: %w", section, name, cause)
	case strings.ContainsRune(name, '\\'):
		return fmt.Errorf("%s: %q holds a backslash: %w", section, name, cause)
	case len(name) > maxHookComponent:
		return fmt.Errorf("%s: name is %d bytes, over the %d cap: %w",
			section, len(name), maxHookComponent, cause)
	}
	return nil
}

// RelativizeHookArgv divides one argv row's script into the two halves the
// shape keeps apart. A script inside the config root becomes a root-relative
// name; anything else is this machine's and is returned as local.
func RelativizeHookArgv(root, script string) (rel string, local bool) {
	if !filepath.IsAbs(script) {
		return filepath.ToSlash(filepath.Clean(script)), false
	}
	cleanRoot := filepath.Clean(root)
	inside, err := filepath.Rel(cleanRoot, filepath.Clean(script))
	if err != nil {
		return "", true
	}
	if inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) || filepath.IsAbs(inside) {
		return "", true
	}
	return filepath.ToSlash(inside), false
}

// NormalizeHookDoc is the one idempotent step that moves an argv row naming
// something outside the config root out of the shared body, and rewrites the
// shared rows as root-relative. moved is whether anything changed, so a
// caller can tell a second run from a first.
func (s *Store) NormalizeHookDoc(doc HookDoc) (HookDoc, bool, error) {
	if doc.Root == "" {
		return doc, false, nil
	}
	out := HookDoc{
		Events: map[string][][]string{},
		Files:  map[string]HookFile{},
		Local:  map[string][][]string{},
		Root:   doc.Root,
	}
	for rel, file := range doc.Files {
		out.Files[rel] = file
	}
	for _, event := range sortedEvents(doc.Events) {
		for _, row := range doc.Events[event] {
			if len(row) == 0 {
				out.Events[event] = append(out.Events[event], row)
				continue
			}
			rel, local := RelativizeHookArgv(doc.Root, row[0])
			next := append([]string(nil), row...)
			if local {
				out.Local[event] = append(out.Local[event], next)
				continue
			}
			next[0] = rel
			out.Events[event] = append(out.Events[event], next)
		}
	}
	for _, event := range sortedEvents(doc.Local) {
		out.Local[event] = append(out.Local[event], doc.Local[event]...)
	}
	before, err := json.Marshal(hookValue{Events: doc.Events, Files: hookFileValues(doc.Files), Local: doc.Local})
	if err != nil {
		return doc, false, err
	}
	after, err := json.Marshal(hookValue{Events: out.Events, Files: hookFileValues(out.Files), Local: out.Local})
	if err != nil {
		return doc, false, err
	}
	return out, !sameJSON(before, after), nil
}

// HooksMapOf is the dispatcher-facing view of a hooks document: the shared
// argv rows under the root-relative names they are stored with, and the
// machine rows under the absolute names they always keep. The two are one map,
// because the dispatcher runs both and cannot tell them apart.
func HooksMapOf(doc HookDoc) HooksMap {
	out := HooksMap{}
	for _, event := range sortedEvents(doc.Events) {
		out[event] = append(out[event], doc.Events[event]...)
	}
	for _, event := range sortedEvents(doc.Local) {
		out[event] = append(out[event], doc.Local[event]...)
	}
	return out
}

func hookFileValues(files map[string]HookFile) map[string]hookFileValue {
	out := make(map[string]hookFileValue, len(files))
	for name, file := range files {
		out[name] = hookFileValue{Contents: base64.StdEncoding.EncodeToString(file.Contents), Exec: file.Exec}
	}
	return out
}

func sortedEvents(events map[string][][]string) []string {
	names := make([]string, 0, len(events))
	for name := range events {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func sortedFiles(files map[string]HookFile) []string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// readLimited reads at most limit bytes and refuses a file longer than it,
// rather than loading an unbounded file into the document.
func readLimited(path string, limit int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, fmt.Errorf("hooks: %s is over the %d byte cap: %w", path, limit, ErrInvalidValue)
	}
	return data, nil
}
