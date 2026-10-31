package relevo

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
)

// AddServer validates e as the entry for name and returns the servers section
// with it added. Only the servers section changes.
func AddServer(d ConfigDoc, name string, e remote.ServerEntry) (ConfigEdit, error) {
	if err := checkServerName(name); err != nil {
		return ConfigEdit{}, err
	}
	if _, taken := d.Servers[name]; taken {
		return ConfigEdit{}, &FieldError{"name", "server " + name + " already exists"}
	}
	if err := checkServerEntry(name, e); err != nil {
		return ConfigEdit{}, err
	}

	next := copyServers(d.Servers)
	next[name] = e
	return serverEdit(next, name, "add server "+name)
}

// EditServer replaces the entry named name with e, validated like an add. The
// name cannot change, so the entry it addresses is the one it rewrites.
func EditServer(d ConfigDoc, name string, e remote.ServerEntry) (ConfigEdit, error) {
	if _, ok := d.Servers[name]; !ok {
		return ConfigEdit{}, &FieldError{"name", "no server named " + name}
	}
	if err := checkServerEntry(name, e); err != nil {
		return ConfigEdit{}, err
	}

	next := copyServers(d.Servers)
	next[name] = e
	return serverEdit(next, name, "edit server "+name)
}

// DeleteServer removes the server named name. An actor whose placement names
// it refuses the delete and is named, so the form says why before the write is
// attempted; the config cross-check is the backstop.
func DeleteServer(d ConfigDoc, name string) (ConfigEdit, error) {
	if _, ok := d.Servers[name]; !ok {
		return ConfigEdit{}, &FieldError{"name", "no server named " + name}
	}
	if users := actorsPlacedOn(d, name); len(users) > 0 {
		return ConfigEdit{}, &FieldError{"", "placed by " + strings.Join(users, ", ") + "; change their placement in :actors first"}
	}

	next := copyServers(d.Servers)
	delete(next, name)
	return serverEdit(next, name, "delete server "+name)
}

// serverEdit builds a servers-only edit from servers, encoded the way the
// store's own writer encodes it.
func serverEdit(servers map[string]remote.ServerEntry, name, message string) (ConfigEdit, error) {
	body, err := encodeServers(servers)
	if err != nil {
		return ConfigEdit{}, err
	}
	return ConfigEdit{
		Sections: map[config.Section]json.RawMessage{config.Servers: body},
		Message:  message,
		Name:     name,
	}, nil
}

// encodeServers renders the servers section the way the store does: keys
// sorted, two-space indent and a trailing newline.
func encodeServers(servers map[string]remote.ServerEntry) ([]byte, error) {
	if servers == nil {
		servers = map[string]remote.ServerEntry{}
	}
	return client.EncodeServers(remote.Servers(servers))
}

// checkServerName refuses a name the servers section cannot key: an empty one,
// and local, which is the placement sentinel rather than a machine.
func checkServerName(name string) error {
	if strings.TrimSpace(name) == "" {
		return &FieldError{"name", "a name is required"}
	}
	if name == "local" {
		return &FieldError{"name", "local is reserved for placement"}
	}
	return nil
}

// checkServerEntry validates one entry the way the store will, by parsing the
// one-entry body the write would store: a url with a scheme and host, and a
// trust setting that matches it.
func checkServerEntry(name string, e remote.ServerEntry) error {
	body, err := json.Marshal(map[string]remote.ServerEntry{name: e})
	if err != nil {
		return err
	}
	if _, err := remote.ParseServers(body); err != nil {
		return &FieldError{"url", strings.TrimPrefix(err.Error(), "parse servers file: ")}
	}
	return nil
}

// actorsPlacedOn names every actor whose placement lists server, sorted.
func actorsPlacedOn(d ConfigDoc, server string) []string {
	var out []string
	for _, name := range sortedActorNames(d.Actors) {
		if slices.Contains(d.Actors[name].Placement, server) {
			out = append(out, name)
		}
	}
	return out
}

// copyServers returns a shallow copy of m: the map is fresh, the entry values
// are shared until a caller replaces one.
func copyServers(m map[string]remote.ServerEntry) map[string]remote.ServerEntry {
	out := make(map[string]remote.ServerEntry, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
