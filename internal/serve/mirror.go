package serve

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/fuad-daoud/relevo/internal/relevo"
)

// ownerMirrorInterval paces the mirror. Its rows answer questions about rounds
// already finished, so they can lag a close by this much.
const ownerMirrorInterval = 15 * time.Second

// mirrorLoop mirrors every owner until ctx ends. It runs beside the reconcile
// ticks, not in them: the first pass over a long log, or another writer holding
// the database, must never delay admitting a round.
func (s *Server) mirrorLoop(ctx context.Context) {
	every := s.mirrorEvery
	if every == 0 {
		every = ownerMirrorInterval
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	since := time.Now()
	mirrors := map[string]*relevo.OwnerMirror{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.mirrorPass(ctx, mirrors, since)
		}
	}
}

// mirrorPass runs one pass over every owner, adding a mirror for an owner it
// has not seen. The mirrors live in the caller's map, because what makes a
// later pass cheap is what each one remembers.
func (s *Server) mirrorPass(ctx context.Context, mirrors map[string]*relevo.OwnerMirror, since time.Time) {
	owners, err := s.ownerRoots()
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("mirror: list owners", "err", err)
		}
		return
	}
	for _, owner := range owners {
		m, ok := mirrors[owner.root]
		if !ok {
			rt := s.runtimeAt(owner.root)
			rt.DB = s.cfg.DB
			m = relevo.NewOwnerMirror(rt, since)
			mirrors[owner.root] = m
		}
		m.Pass(ctx)
	}
}
