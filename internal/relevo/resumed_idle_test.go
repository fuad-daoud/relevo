package relevo

import (
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

func TestResumedIdleNoteWrittenOncePerEpisode(t *testing.T) {
	t.Parallel()

	rt, b := seedClaudeHeadless(t, newFakeRunner())
	now := baseTime.Add(time.Hour)
	b.Progress = &store.Progress{TreeAt: now.Add(-10 * time.Minute), ResumedAt: now.Add(-11 * time.Minute)}
	on, off := b, b
	on.ExploringSince = now.Add(-10 * time.Minute)
	off.ExploringSince = time.Time{}

	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		for _, pair := range [][2]store.Binding{{off, on}, {on, on}, {on, off}, {off, on}} {
			if err := noteResumedIdle(tx, pair[0], pair[1], now); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("noteResumedIdle: %v", err)
	}

	entries, err := rt.Store.ReadLog(b.Name)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	n := 0
	for _, e := range entries {
		if e.Kind == store.KindSwitch && strings.HasPrefix(e.Note, resumedIdleNotePrefix) {
			n++
		}
	}
	if n != 2 {
		t.Errorf("resumed-idle entries = %d, want 2 (one per episode)", n)
	}
}
