package view

import (
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// BindingShape is the shape a status row carries: store.ShapeReader for a
// reader, "" for a writer. A writer's row keeps the document it always had --
// an absent shape reads as a writer everywhere -- while a reader names itself
// so the row's word can follow it.
func BindingShape(b store.Binding) string {
	if b.Shape == store.ShapeReader {
		return store.ShapeReader
	}
	return ""
}

// ProgressDue reports whether a binding's progress sources are due to be
// sampled at now. A binding that has never sampled is due at once; every
// later sample waits out the interval, which the progress clock measures from
// SampledAt. Callers gate the read with it, so a tick inside the interval
// costs no git call at all -- the clock's own cadence check stays as a
// harmless second guard for direct callers.
func ProgressDue(b store.Binding, now time.Time, interval time.Duration) bool {
	if b.Progress == nil {
		return true
	}
	return now.Sub(b.Progress.SampledAt) >= interval
}

// LabelsOf renders a binding's progress labels for `relevo status` and
// `relevo ui`: the working word that replaces "working", and the stale
// word that sits after the state word. Either may be "".
func LabelsOf(b store.Binding, now time.Time) (working string, stale string) {
	switch {
	case !b.StalledSince.IsZero():
		working = "stalled " + AgeText(now.Sub(b.StalledSince))
	case !b.ExploringSince.IsZero():
		working = "exploring " + AgeText(now.Sub(b.ExploringSince))
	}
	if !b.StaleSince.IsZero() {
		stale = "stale " + AgeText(now.Sub(b.StaleSince))
	}
	return working, stale
}
