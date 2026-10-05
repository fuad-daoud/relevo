package ui

import (
	"fmt"
	"testing"

	"github.com/fuad-daoud/relevo/internal/view"
)

// The frame benchmark is the measuring instrument for the render phases: the
// four steps one fleet frame costs, at the two row counts that bracket a real
// fleet. It asserts nothing and pins nothing -- it exists to produce numbers,
// and it stays in the tree for the rest of that work.

const (
	frameBenchWidth  = 160
	frameBenchHeight = 50
)

// The sinks keep every benchmarked value reachable, so the compiler cannot
// drop the work the numbers are supposed to measure.
var (
	frameSink      string
	frameRowsSink  []view.BindingStatus
	frameLinesSink []fleetLine
)

// frameBenchEnv is a loaded Env at the benchmark terminal size whose report
// holds n rows. The rows come from allStatesRows -- the golden fixture -- cycled
// and renamed, so both row counts carry the same mix of display states,
// routes, usage and headless facts the goldens read.
func frameBenchEnv(n int) Env {
	seed := allStatesRows()
	rows := make([]view.BindingStatus, 0, n)
	for i := range n {
		r := seed[i%len(seed)]
		r.Name = fmt.Sprintf("%s-%d", r.Name, i/len(seed))
		rows = append(rows, r)
	}
	return Env{
		Loaded: true,
		Report: view.Report{Bindings: rows},
		Now:    railNow,
		Width:  frameBenchWidth,
		Height: frameBenchHeight,
	}
}

// BenchmarkFrame measures Body, rows, fleetListLines and view.SortRows at 30 and
// 64 rows on a 160x50 frame, with allocations reported for each.
func BenchmarkFrame(b *testing.B) {
	for _, n := range []int{30, 64} {
		env := frameBenchEnv(n)
		f := newFleetView(true)

		b.Run(fmt.Sprintf("Body/rows=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				frameSink = f.Body(env, frameBenchWidth, frameBenchHeight)
			}
		})

		b.Run(fmt.Sprintf("rows/rows=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				frameRowsSink = f.rows(env)
			}
		})

		b.Run(fmt.Sprintf("fleetListLines/rows=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				frameLinesSink = f.fleetListLines(env, frameBenchWidth)
			}
		})

		b.Run(fmt.Sprintf("SortRows/rows=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				frameRowsSink = view.SortRows(env.Report.Bindings, true)
			}
		})
	}
}
