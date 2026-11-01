package config

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"

	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// A native agents entry names an agent each harness already knows how to run. That
// names the file but does not carry it, so the `files` member beside it carries the
// bytes, one per kind. Three rules shape the member: a kind is a harness kind and an
// agent is one plain component, with the directory computed here and in the applier
// so an entry cannot steer a write; only the kinds the `native` map names may carry
// a file; a kind the entry names and the member does not is filled in by the capture.
// A source agent carries no file: its definitions derive from its source text.

// agentsFilesMember is the reserved member of an agents item that carries the
// harness files a native entry names.
const agentsFilesMember = "files"

// Caps. One file over the first is refused on its own; an item over the second is
// refused whole, because a peer would have to carry it. They are the hooks caps:
// a text a peer cannot carry is not one to send.
const (
	// MaxAgentFileBytes is one embedded harness file.
	MaxAgentFileBytes = 256 << 10
	// MaxAgentsValueBytes is one agents item: every entry and every file it
	// carries.
	MaxAgentsValueBytes = 2 << 20
)

// ErrAgentPath marks a refusal of a name or a file: what the item asked to be
// written is refused, and nothing of the store has failed.
var ErrAgentPath = errors.New("agents: unsafe path")

// AgentFile is one native agent's harness file for one harness kind, as the
// document carries it.
type AgentFile struct {
	Kind     string
	Contents []byte
}

// Hash is the file's content digest: the identity the hash cache keeps and the
// applier compares against before it overwrites anything.
func (f AgentFile) Hash() string {
	sum := sha256.Sum256(f.Contents)
	return hex.EncodeToString(sum[:])
}

// AgentEntryDoc is one agents item as the document carries it: the entry and its
// native kinds' harness files, with the item's own key kept beside it for checking.
type AgentEntryDoc struct {
	Name  string
	Entry roles.AgentEntry
	Files map[string]AgentFile
}

// Native reports whether the entry is native, the only kind carrying harness files.
func (d AgentEntryDoc) Native() bool { return d.Entry.Source == "" && len(d.Entry.Native) > 0 }

// Kinds is the entry's own kinds in name order.
func (d AgentEntryDoc) Kinds() []string {
	names := make([]string, 0, len(d.Entry.Native))
	for kind := range d.Entry.Native {
		names = append(names, kind)
	}
	sort.Strings(names)
	return names
}

// ParseAgentEntryDoc reads one agents item. An unnamed member stays in the body
// untouched: the item's other members are the config's business.
func ParseAgentEntryDoc(name string, body json.RawMessage) (AgentEntryDoc, error) {
	doc := AgentEntryDoc{Name: name, Files: map[string]AgentFile{}}
	if len(body) == 0 {
		return doc, nil
	}
	if err := json.Unmarshal(body, &doc.Entry); err != nil {
		return AgentEntryDoc{}, fmt.Errorf("%s: %s: %w", Agents, name, err)
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(body, &members); err != nil || members == nil {
		return doc, nil
	}
	raw, ok := members[agentsFilesMember]
	if !ok {
		return doc, nil
	}
	var files map[string]string
	if err := json.Unmarshal(raw, &files); err != nil {
		return AgentEntryDoc{}, fmt.Errorf("%s: %s: %s is not a kind-to-file map: %w",
			Agents, name, agentsFilesMember, err)
	}
	for kind, encoded := range files {
		contents, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return AgentEntryDoc{}, fmt.Errorf("%s: %s: %s: %s is not base64: %w",
				Agents, name, agentsFilesMember, kind, err)
		}
		doc.Files[kind] = AgentFile{Kind: kind, Contents: contents}
	}
	return doc, nil
}

// Encode is the item as the config doc stores it: every member the entry carried,
// with the files member set to doc's own files. Bytes are base64 because JSON text
// cannot carry a non-UTF-8 byte string without silently changing it. An entry with no
// files is written back without the member at all, so a source agent's item stays the
// bytes it was before.
func (d AgentEntryDoc) Encode() (json.RawMessage, error) {
	if len(d.Files) == 0 {
		return d.entryOnly()
	}
	members, err := d.members()
	if err != nil {
		return nil, err
	}
	encoded := make(map[string]string, len(d.Files))
	for kind, file := range d.Files {
		encoded[kind] = base64.StdEncoding.EncodeToString(file.Contents)
	}
	files, err := json.Marshal(encoded)
	if err != nil {
		return nil, err
	}
	members[agentsFilesMember] = files
	body, err := json.Marshal(members)
	if err != nil {
		return nil, err
	}
	return checkAgentItemSize(d.Name, body)
}

// entryOnly is the item with the files member dropped, which is what an entry
// that carries none is stored as.
func (d AgentEntryDoc) entryOnly() (json.RawMessage, error) {
	members, err := d.members()
	if err != nil {
		return nil, err
	}
	delete(members, agentsFilesMember)
	body, err := json.Marshal(members)
	if err != nil {
		return nil, err
	}
	return checkAgentItemSize(d.Name, body)
}

// members is the item as an object of raw members, so the files member is replaced
// without rebuilding the entry's own members.
func (d AgentEntryDoc) members() (map[string]json.RawMessage, error) {
	body, err := json.Marshal(d.Entry)
	if err != nil {
		return nil, err
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(body, &members); err != nil {
		return nil, err
	}
	if members == nil {
		members = map[string]json.RawMessage{}
	}
	return members, nil
}

// checkAgentItemSize refuses an item over the whole-item cap.
func checkAgentItemSize(name string, body []byte) (json.RawMessage, error) {
	if len(body) > MaxAgentsValueBytes {
		return nil, fmt.Errorf("%s: %s: value is %d bytes, over the %d cap: %w",
			Agents, name, len(body), MaxAgentsValueBytes, ErrInvalidValue)
	}
	return body, nil
}

// ValidateAgentEntryDoc refuses one rule per refusal: a non-harness kind, a
// non-plain-component agent name, a file whose kind the native map does not name,
// a file over the per-file cap and an item over the whole-item cap.
func ValidateAgentEntryDoc(doc AgentEntryDoc) error {
	if err := safeAgentComponent(doc.Name); err != nil {
		return fmt.Errorf("%s: %w", Agents, err)
	}
	for _, kind := range doc.Kinds() {
		if _, ok := harness.Lookup(kind); !ok {
			return fmt.Errorf("%s: %s: native.%s: unknown harness kind: %w",
				Agents, doc.Name, kind, ErrAgentPath)
		}
		if err := safeAgentComponent(doc.Entry.Native[kind].Agent); err != nil {
			return fmt.Errorf("%s: %s: native.%s: %w", Agents, doc.Name, kind, err)
		}
	}
	for _, kind := range sortedAgentKinds(doc.Files) {
		file := doc.Files[kind]
		if _, ok := harness.Lookup(kind); !ok {
			return fmt.Errorf("%s: %s: files: %q is not a harness kind: %w",
				Agents, doc.Name, kind, ErrAgentPath)
		}
		if _, named := doc.Entry.Native[kind]; !named {
			return fmt.Errorf("%s: %s: files: %q is not a kind this agent has: %w",
				Agents, doc.Name, kind, ErrAgentPath)
		}
		if len(file.Contents) > MaxAgentFileBytes {
			return fmt.Errorf("%s: %s: files: %s: %d bytes, over the %d cap: %w",
				Agents, doc.Name, kind, len(file.Contents), MaxAgentFileBytes, ErrInvalidValue)
		}
	}
	_, err := doc.Encode()
	return err
}

// SafeAgentComponent refuses a name that is not one plain path component, so the
// applier and the document agree on one rule.
func SafeAgentComponent(name string) error { return safeAgentComponent(name) }

// safeAgentComponent refuses a name that is not one plain path component, sharing the
// hook names' rule so an agent name and a script name cannot be safe by different lights.
func safeAgentComponent(name string) error {
	return safeNamedComponent("agents", name, ErrAgentPath)
}

func sortedAgentKinds(files map[string]AgentFile) []string {
	names := make([]string, 0, len(files))
	for kind := range files {
		names = append(names, kind)
	}
	sort.Strings(names)
	return names
}

// agentFileValue is one cache entry: what the capture last saw of a harness file on
// disk. The digest is what the applier compares; size and modification time make an
// unchanged file cost a stat instead of a read.
type agentFileValue struct {
	Hash string `json:"h"`
	Size int64  `json:"s"`
	Mod  int64  `json:"m"`
}

func (v agentFileValue) matches(info os.FileInfo) bool {
	return v.Size == info.Size() && v.Mod == info.ModTime().UnixNano()
}
