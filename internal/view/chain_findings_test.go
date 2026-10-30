package view

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/workflow"
)

func TestFlowFindingsReadsTheNewestScan(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		results map[string]workflow.Result
		want    int
	}{
		{"no scan", map[string]workflow.Result{"build": {Round: 2}}, 0},
		{"newest round wins", map[string]workflow.Result{
			"scan-a": {Round: 3, Outcomes: map[string]string{"findings": "4"}},
			"scan-b": {Round: 1, Outcomes: map[string]string{"findings": "9"}},
		}, 4},
		{"a tie goes to the later step name", map[string]workflow.Result{
			"scan-a": {Round: 2, Outcomes: map[string]string{"findings": "4"}},
			"scan-b": {Round: 2, Outcomes: map[string]string{"findings": "9"}},
		}, 9},
		{"an unreadable count is skipped", map[string]workflow.Result{
			"scan-a": {Round: 1, Outcomes: map[string]string{"findings": "2"}},
			"scan-b": {Round: 5, Outcomes: map[string]string{"findings": "-1"}},
			"scan-c": {Round: 6, Outcomes: map[string]string{"findings": "many"}},
		}, 2},
	}
	for _, c := range cases {
		if got := FlowFindings(workflow.State{Results: c.results}); got != c.want {
			t.Errorf("%s: FlowFindings = %d, want %d", c.name, got, c.want)
		}
	}
}
