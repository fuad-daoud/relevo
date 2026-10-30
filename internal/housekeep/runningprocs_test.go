package housekeep

import (
	"reflect"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

// runningProcsCase is one collector row: the bindings in and the tuples out.
type runningProcsCase struct {
	name string
	bs   []store.Binding
	want []RunningProcRef
}

// checkRunningProcs runs the table: a local headless builder, the round's
// gate, and running headless consults are gathered; anything else is not.
func checkRunningProcs(t *testing.T, cases []runningProcsCase) {
	t.Helper()

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := RunningProcs(c.bs)
			if len(got) == 0 && len(c.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("RunningProcs() = %+v; want %+v", got, c.want)
			}
		})
	}
}

// TestRunningProcsGathersLocalHeads pins the gathered rows: a local headless
// builder, the round's gate, a running headless consult, and binding order
// across several bindings.
func TestRunningProcsGathersLocalHeads(t *testing.T) {
	t.Parallel()

	checkRunningProcs(t, []runningProcsCase{
		{
			name: "a headless builder and its gate",
			bs: []store.Binding{{
				Name:    "web",
				Builder: store.Endpoint{Mode: store.ModeHeadless, PID: 11},
				GateRun: &store.GateRun{PID: 12},
			}},
			want: []RunningProcRef{
				{Binding: "web", Kind: "builder", PID: 11},
				{Binding: "web", Kind: "gate", PID: 12},
			},
		},
		{
			name: "a running headless consult is gathered",
			bs: []store.Binding{{
				Name: "web",
				Consults: []store.Consult{{
					State:    store.ConsultRunning,
					Endpoint: store.Endpoint{Mode: store.ModeHeadless, PID: 13},
				}},
			}},
			want: []RunningProcRef{{Binding: "web", Kind: "consult", PID: 13}},
		},
		{
			name: "several bindings keep binding order",
			bs: []store.Binding{
				{Name: "b", Builder: store.Endpoint{Mode: store.ModeHeadless, PID: 2}},
				{Name: "a", Builder: store.Endpoint{Mode: store.ModeHeadless, PID: 1}},
			},
			want: []RunningProcRef{
				{Binding: "b", Kind: "builder", PID: 2},
				{Binding: "a", Kind: "builder", PID: 1},
			},
		},
	})
}

// TestRunningProcsSkipsForeignBindings pins the binding-level exclusions: a
// served (owner-carrying) or remote binding contributes nothing.
func TestRunningProcsSkipsForeignBindings(t *testing.T) {
	t.Parallel()

	checkRunningProcs(t, []runningProcsCase{
		{
			name: "a served binding is excluded",
			bs: []store.Binding{{
				Name:    "web",
				Owner:   "client1",
				Builder: store.Endpoint{Mode: store.ModeHeadless, PID: 21},
			}},
			want: nil,
		},
		{
			name: "a remote binding is excluded",
			bs: []store.Binding{{
				Name:    "web",
				Builder: store.Endpoint{Mode: store.ModeRemote, PID: 31},
			}},
			want: nil,
		},
	})
}

// TestRunningProcsSkipsIdleProcesses pins the process-level exclusions: a
// pane builder, a zero pid, a non-running consult, and a running pane
// consult are not gathered.
func TestRunningProcsSkipsIdleProcesses(t *testing.T) {
	t.Parallel()

	checkRunningProcs(t, []runningProcsCase{
		{
			name: "a pane builder is excluded",
			bs: []store.Binding{{
				Name:    "web",
				Builder: store.Endpoint{Mode: "", PID: 41},
			}},
			want: nil,
		},
		{
			name: "a zero pid is excluded",
			bs: []store.Binding{{
				Name:    "web",
				Builder: store.Endpoint{Mode: store.ModeHeadless, PID: 0},
				GateRun: &store.GateRun{PID: 0},
			}},
			want: nil,
		},
		{
			name: "a done consult is excluded",
			bs: []store.Binding{{
				Name: "web",
				Consults: []store.Consult{
					{State: store.ConsultDone, Endpoint: store.Endpoint{Mode: store.ModeHeadless, PID: 14}},
					{State: store.ConsultSilent, Endpoint: store.Endpoint{Mode: store.ModeHeadless, PID: 15}},
					{State: store.ConsultSpawning, Endpoint: store.Endpoint{Mode: store.ModeHeadless, PID: 16}},
				},
			}},
			want: nil,
		},
		{
			name: "a running pane consult is excluded",
			bs: []store.Binding{{
				Name: "web",
				Consults: []store.Consult{{
					State:    store.ConsultRunning,
					Endpoint: store.Endpoint{Mode: "", PID: 17},
				}},
			}},
			want: nil,
		},
	})
}
