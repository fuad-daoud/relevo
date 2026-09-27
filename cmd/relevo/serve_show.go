package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/serve"
	"github.com/fuad-daoud/relevo/internal/store"
)

// serveShowUsage is the removed `relevo serve show` usage line, updated to
// the verb that carries the --owner route now (§4.1). cmdShow prints it when
// an --owner invocation names more than one section.
const serveShowUsage = "usage: relevo show <name> --owner <label|id> [--round N] [--prompt|--report|--diff|--drift|--log|--transcript|--output|--artifacts] [--json] [--state <dir>]"

// serveLog is cmdServeLog's body, moved so `relevo show <name> --owner
// <label> --log` calls it (§4.1). It takes the parsed values: state is the
// resolved --state ("" for the default root). It prints one owner's binding
// log from the server (#216): the read-only counterpart of `relevo log`. It
// resolves the owner with the same resolver `serve unbind` uses, builds that
// owner's runtime, and renders through the same printLog the client verb
// uses, so the output reads exactly like a client's. It stamps nothing -- the
// .viewed sidecar is the owner's, not the admin's -- and creates nothing.
func serveLog(owner, state, name string, round, after int, asJSON, follow bool) error {
	root, d, err := adminRootFor(state)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()

	srv, err := serve.New(serveAdminConfig(root, d))
	if err != nil {
		return err
	}

	rt, _, err := serve.AdminOwnerRuntime(srv, owner)
	if err != nil {
		if errors.Is(err, serve.ErrNoSuchClient) {
			fmt.Fprintf(os.Stderr, "relevo serve log: no such client: %s\n", owner)
		} else if errors.Is(err, store.ErrNotFound) {
			fmt.Fprintf(os.Stderr, "relevo serve log: %s/%s: binding not found\n", owner, name)
		} else {
			fmt.Fprintf(os.Stderr, "relevo serve log: %v\n", err)
		}
		return exitCodeErr{code: 1}
	}

	if err := printLog(rt, name, round, after, asJSON, follow, false); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			fmt.Fprintf(os.Stderr, "relevo serve log: %s/%s: binding not found\n", owner, name)
		} else {
			fmt.Fprintf(os.Stderr, "relevo serve log: %v\n", err)
		}
		return exitCodeErr{code: 1}
	}
	return nil
}

// serveShow is cmdServeShow's body, moved so `relevo show <name> --owner
// <label>` calls it (§4.1). It takes the parsed values: state is the resolved
// --state ("" for the default root) and section the resolved section. It
// prints one owner's round from the server (#216): the read-only counterpart
// of `relevo show`. It reads live bindings only -- opening the database would
// create it, and the database belongs to the client that ran the work, not to
// the server admin's read -- and prefixes the stderr header with the owner's
// label so the reader can see whose round it is.
func serveShow(owner, state, name string, round int, section relevo.ShowSection, asJSON bool, artifactRel string) error {
	root, d, err := adminRootFor(state)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()

	srv, err := serve.New(serveAdminConfig(root, d))
	if err != nil {
		return err
	}

	rt, label, err := serve.AdminOwnerRuntime(srv, owner)
	if err != nil {
		if errors.Is(err, serve.ErrNoSuchClient) {
			fmt.Fprintf(os.Stderr, "relevo serve show: no such client: %s\n", owner)
		} else if errors.Is(err, store.ErrNotFound) {
			fmt.Fprintf(os.Stderr, "relevo serve show: %s/%s: binding not found (serve show reads live bindings only)\n", owner, name)
		} else {
			fmt.Fprintf(os.Stderr, "relevo serve show: %v\n", err)
		}
		return exitCodeErr{code: 1}
	}

	opts := relevo.ShowOptions{Name: name, Round: round, Section: section, JSON: asJSON, ArtifactRel: artifactRel}
	if err := printShow(rt, opts, false, false, label+"/"); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			fmt.Fprintf(os.Stderr, "relevo serve show: %s/%s: binding not found (serve show reads live bindings only)\n", owner, name)
		} else {
			fmt.Fprintf(os.Stderr, "relevo serve show: %v\n", err)
		}
		return exitCodeErr{code: 1}
	}
	return nil
}
