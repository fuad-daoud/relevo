package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/serve"
	"github.com/fuad-daoud/relevo/internal/store"
)

// serveLog is the body `relevo show <name> --owner <label> --log` calls: state is
// the resolved --state ("" for the default root). It prints one owner's binding
// log from the server, the read-only counterpart of `relevo log`, rendered
// through the same printLog so it reads exactly like a client's. It stamps
// nothing and creates nothing.
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
		return classifyReadErr(err)
	}
	return nil
}

// serveShow is the body `relevo show <name> --owner <label>` calls: state is the
// resolved --state and section the resolved section. It prints one owner's round
// from the server, the read-only counterpart of `relevo show`. It reads live
// bindings only: opening the database would create it, and the database belongs
// to the client that ran the work. It prefixes the stderr header with the
// owner's label.
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

	// The owner read always peeks: an admin's read of another owner's binding stays
	// read-only and never claims that owner's pending payload.
	opts := relevo.ShowOptions{Name: name, Round: round, Section: section, JSON: asJSON, Peek: true, ArtifactRel: artifactRel}
	if err := printShow(rt, opts, false, false, label+"/"); err != nil {
		return classifyReadErr(err)
	}
	return nil
}
