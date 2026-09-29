package relevo

import (
	"reflect"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

// TestRunningProcs is #370 §4.8's collector table: a local headless builder,
// the round's gate, and running headless consults are gathered; a served
// (owner-carrying) or remote binding, a done consult, a pane process and a
// zero pid are not.
func TestRunningProcs(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		bs   []store.Binding
		want []RunningProcRef
	}{
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
	}
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
