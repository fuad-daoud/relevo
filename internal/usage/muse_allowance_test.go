package usage

import (
	"strconv"
	"strings"
	"testing"
)

func museUsageLineWith(windowUsed, weeklyUsed float64, windowReset, weeklyReset int64) string {
	return `{"payload_type":"run.usage","payload":{"allowance":{"window":{"used_percent":` +
		strconv.FormatFloat(windowUsed, 'f', -1, 64) + `,"resets_at_ms":` + strconv.FormatInt(windowReset, 10) +
		`},"weekly":{"used_percent":` + strconv.FormatFloat(weeklyUsed, 'f', -1, 64) +
		`,"resets_at_ms":` + strconv.FormatInt(weeklyReset, 10) + `}},"models":[]}}`
}

func TestParseMuseAllowanceKeepsTheLastEvent(t *testing.T) {
	data := museUsageLineWith(40, 3, 1792848178000, 1792972800000) + "\n" +
		museUsageLineWith(92, 34, 1792848178000, 1792972800000) + "\n"
	got, ok := ParseMuseAllowance([]byte(data))
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if got.Window.UsedPercent != 92 || got.Weekly.UsedPercent != 34 {
		t.Errorf("allowance = %+v, want the last event's 92/34", got)
	}
	if got.Window.ResetsAtMS != 1792848178000 || got.Weekly.ResetsAtMS != 1792972800000 {
		t.Errorf("allowance = %+v, want the recorded resets", got)
	}
	if !got.Window.OK || !got.Weekly.OK {
		t.Errorf("allowance = %+v, want both dimensions complete", got)
	}
}

func TestParseMuseAllowanceSkipsEventsWithoutAllowance(t *testing.T) {
	data := strings.Join([]string{
		`{"payload_type":"tool.result","payload":{"text":"ok"}}`,
		`{"payload_type":"run.usage","payload":{"models":[]}}`,
		museUsageLineWith(4, 1, 1792848178000, 1792972800000),
		`not json at all`,
		`{"payload_type":"run.terminal.completed","payload":{"text":"done"}}`,
	}, "\n") + "\n"
	got, ok := ParseMuseAllowance([]byte(data))
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if got.Window.UsedPercent != 4 || got.Weekly.UsedPercent != 1 {
		t.Errorf("allowance = %+v, want 4/1 from the one allowance event", got)
	}
}

func TestParseMuseAllowanceReportsNoAllowance(t *testing.T) {
	for _, data := range []string{
		"",
		`{"payload_type":"run.usage","payload":{"models":[]}}` + "\n",
		`{"payload_type":"tool.result","payload":{"text":"ok"}}` + "\n",
	} {
		if _, ok := ParseMuseAllowance([]byte(data)); ok {
			t.Errorf("ok = true for %q, want false", data)
		}
	}
}

func TestParseMuseAllowanceLeavesAnIncompleteDimensionUnset(t *testing.T) {
	data := `{"payload_type":"run.usage","payload":{"allowance":{"window":{"used_percent":90},"weekly":{"used_percent":3,"resets_at_ms":1792972800000}},"models":[]}}` + "\n"
	got, ok := ParseMuseAllowance([]byte(data))
	if !ok {
		t.Fatal("ok = false, want true for the complete weekly dimension")
	}
	if got.Window.OK {
		t.Errorf("window = %+v, want unset when its reset is missing", got.Window)
	}
	if !got.Weekly.OK || got.Weekly.UsedPercent != 3 {
		t.Errorf("weekly = %+v, want the complete 3%% dimension", got.Weekly)
	}
}

func TestParseMuseAllowanceKeepsEachDimensionsLastCompleteEvent(t *testing.T) {
	first := museUsageLineWith(90, 3, 1792848178000, 1792972800000)
	second := `{"payload_type":"run.usage","payload":{"allowance":{"window":{"used_percent":10},"weekly":{"used_percent":34,"resets_at_ms":1792972800000}},"models":[]}}`
	got, ok := ParseMuseAllowance([]byte(first + "\n" + second + "\n"))
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if got.Window.UsedPercent != 90 {
		t.Errorf("window = %+v, want the earlier complete 90, not the later incomplete one", got.Window)
	}
	if got.Weekly.UsedPercent != 34 {
		t.Errorf("weekly = %+v, want the later 34", got.Weekly)
	}
}
