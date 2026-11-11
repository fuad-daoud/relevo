package serve

import (
	"fmt"
	"os"

	"github.com/fuad-daoud/relevo/internal/isolate"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/uplink"
)

// The serve-side relevo.Runtime wiring: one owner-rooted runtime per caller,
// with the server's gates, hooks and node views installed. Isolation
// rewrites the runner (and the transports that shell out) per mode; every
// other field is shared.

func (s *Server) runtime(id remote.ClientID) (relevo.Runtime, error) {
	root, err := s.ownerRoot(id)
	if err != nil {
		return relevo.Runtime{}, err
	}
	return s.runtimeAt(root), nil
}

func (s *Server) runtimeAt(root string) relevo.Runtime {
	// One copy for the whole runtime: the candidates, policy, registry and
	// accounts a pick sees all come from the same reload.
	cfg := s.live()
	st := s.ownerStore(root)
	rt := relevo.Runtime{
		Git:        cfg.Git,
		Runner:     cfg.Runner,
		Store:      st,
		Transport:  s.transport,
		Candidates: cfg.Candidates,
		Accounts:   cfg.Accounts,
		Policy:     cfg.Policy,
		Gates:      s.gates,      // server-wide, not the owner's own
		GatesLocal: s.gatesLocal, // plus this host's own ledger row: served picks and switches honour `relevo gate` here
		InFlight:   s.inFlightFor(root),
		History:    s.cfg.DB,
		Usage:      cfg.Usage,
		Prices:     cfg.Prices,
		Now:        cfg.Now,
		StartedAt:  cfg.StartedAt,
		Roles:      cfg.Roles,
		Registry:   cfg.Registry,
		Hooks:      cfg.Hooks,
		DiskFree:   cfg.DiskFree,
		Scope:      cfg.Scope,
		HeldCPUs:   func(tx *store.Tx, self string) ([]int, error) { return s.heldCPUs(root, tx, self) },
		// The reaper is server-wide: deleting an owner's abandoned session
		// runs the harness binary, which is the same for every owner.
		SessionReaper: cfg.SessionReaper,
	}
	// Every mode that runs builders on this host gives the owner its own tmp
	// root, so one owner's sweep can never see another owner's entries.
	// Container mode gets none: applyContainer's builder sees the container's
	// own /tmp, and a host path means nothing inside it.
	if cfg.Isolation != isolate.ModeContainer {
		rt.TmpRoot = s.ownerTmpDir(root)
	}
	if cfg.Isolation == isolate.ModeNone {
		// The hostile check runs on every resolution, never once: /tmp is
		// aged and writable by every tenant, so a link that was fine can
		// turn planted while serve keeps running. User mode does no
		// pre-create here -- a serve-owned target would fail the
		// tenant's own check -- its creation lives in ensureTenantRoots.
		// A refusal wraps the runner so the round start halts NEEDS YOU;
		// TmpRoot stays set at the link path, since an empty root would
		// read as container "no tmp" and silently inherit TMPDIR.
		target := s.ownerTmpTarget(root)
		if err := os.MkdirAll(target, tenantDirMode); err != nil {
			rt.Runner = isolate.Refuse(rt.Runner, fmt.Errorf("tmp target %s: create it with mode 0700: %w", target, err))
		} else if err := ensureTmpLink(rt.TmpRoot, target, uint32(os.Geteuid())); err != nil {
			rt.Runner = isolate.Refuse(rt.Runner, err)
		}
	}
	switch cfg.Isolation {
	case isolate.ModeUser:
		s.applyTenant(root, st, &rt)
	case isolate.ModeContainer:
		s.applyContainer(root, &rt)
	}
	rt.CloseServedRound = CloseServedRound
	// The remote send runs above the engine: the served runtime ships through
	// the same uplink functions as the CLI runtime.
	rt.RemoteSend = relevo.RemoteSendDeps{
		Send:   uplink.SendRemote,
		Ship:   uplink.Ship,
		Record: uplink.Record,
	}
	return rt
}

// applyContainer rewires rt for one container-mode owner: its runner starts
// every process as a rootless podman container bound to the owner's repo, so a
// round, gate, consult or owner-path git runs confined by the container rather
// than directly as the serve uid. There is no tenant chown and no tenant root:
// the container runs as the serve uid on the host via keep-id. A runner that is
// not a Boundary is wrapped to refuse every Start, so container mode never falls
// back to the serve uid.
func (s *Server) applyContainer(root string, rt *relevo.Runtime) {
	repo, err := s.repoRoot(ownerIDOf(root))
	if err != nil {
		rt.Runner = isolate.Refuse(s.cfg.Runner, err)
		return
	}
	if b, ok := s.cfg.Runner.(isolate.Boundary); ok {
		rt.Runner = b.ForContainer(isolate.ContainerSpec{
			Image:      s.cfg.IsolationImage,
			RepoRoot:   repo,
			GitShimDir: rt.Store.GitShimDir(),
		})
		return
	}
	rt.Runner = isolate.Refuse(s.cfg.Runner, fmt.Errorf("serve.isolation=container needs a container boundary runner, got %T", s.cfg.Runner))
}

// applyTenant rewires rt for one user-mode owner: the boundary refuses when the
// tenant could not be resolved, and otherwise every process the owner starts --
// rounds, gates, consults, owner-path git and the session reaper -- runs as the
// tenant, with the tenant's own environment and a per-owner bundle transport.
// A runner that is not a Boundary is wrapped to refuse every Start, so user
// mode never falls back to the serve uid. ensureTenantRoots runs on every
// resolution because the daemon's prunes remove empty tenant directories.
func (s *Server) applyTenant(root string, st *store.Store, rt *relevo.Runtime) {
	owner := ownerIDOf(root)
	t, err := s.tenantFor(owner)
	if b, ok := s.cfg.Runner.(isolate.Boundary); ok {
		rt.Runner = withTmpDir(b.ForTenant(t, err), s.ownerTmpDir(root))
	} else {
		// A runner that is not a tenant boundary is a wiring fault; it fails
		// closed with a boundary-setup error, so a round halts NEEDS YOU
		// rather than running as the serve uid.
		rt.Runner = isolate.Refuse(s.cfg.Runner, fmt.Errorf("serve.isolation=user needs a tenant boundary runner, got %T", s.cfg.Runner))
	}
	if t == nil {
		st.SetTenantChown(nil)
		return
	}
	gc := s.cfg.Git.WithCredential(t.UID, t.GID, tenantEnv(*t))
	rt.Git = gc
	if s.cfg.ReaperFor != nil {
		rt.SessionReaper = s.cfg.ReaperFor(*t)
	}
	rt.Transport = s.ownerTransport(root, *t, gc)
	st.SetTenantChown(func(path string) error {
		return os.Lchown(path, int(t.UID), int(t.GID))
	})
}
