package usage

import (
	"bytes"
	"encoding/json"
)

// MuseAllowanceLevel is one window of the subscription allowance a muse
// stream's run.usage events carry: the used share and the reset bounding it.
// OK is false when the event did not carry both numbers, so a dimension with
// no usable reset never reads as gating.
type MuseAllowanceLevel struct {
	UsedPercent float64
	ResetsAtMS  int64
	OK          bool
}

// MuseAllowance is the last allowance a muse stream reported, per dimension:
// the last run.usage event carrying a complete window sets Window, and the
// last carrying a complete weekly sets Weekly.
type MuseAllowance struct {
	Window MuseAllowanceLevel
	Weekly MuseAllowanceLevel
}

// museAllowanceDim decodes one allowance window tolerantly: a missing number
// stays nil rather than failing the line, so a dimension without a usable
// reset reads as incomplete instead of dropping the other one.
type museAllowanceDim struct {
	UsedPercent *float64 `json:"used_percent"`
	ResetsAtMS  *int64   `json:"resets_at_ms"`
}

type museAllowanceEvent struct {
	PayloadType string `json:"payload_type"`
	Payload     struct {
		Allowance *struct {
			Window *museAllowanceDim `json:"window"`
			Weekly *museAllowanceDim `json:"weekly"`
		} `json:"allowance"`
	} `json:"payload"`
}

func museAllowanceLevel(d *museAllowanceDim) (MuseAllowanceLevel, bool) {
	if d == nil || d.UsedPercent == nil || d.ResetsAtMS == nil {
		return MuseAllowanceLevel{}, false
	}
	return MuseAllowanceLevel{UsedPercent: *d.UsedPercent, ResetsAtMS: *d.ResetsAtMS, OK: true}, true
}

// ParseMuseAllowance returns the last allowance a muse stream reported: the
// allowance of the last run.usage event, per dimension. Lines that are not
// run.usage events, carry no allowance, or fail to parse are skipped. ok is
// false when no event carried a complete dimension.
func ParseMuseAllowance(data []byte) (out MuseAllowance, ok bool) {
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(line) == 0 || line[0] != '{' || len(line) > maxLine {
			continue
		}
		var ev museAllowanceEvent
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		if ev.PayloadType != "run.usage" || ev.Payload.Allowance == nil {
			continue
		}
		if level, good := museAllowanceLevel(ev.Payload.Allowance.Window); good {
			out.Window = level
		}
		if level, good := museAllowanceLevel(ev.Payload.Allowance.Weekly); good {
			out.Weekly = level
		}
	}
	return out, out.Window.OK || out.Weekly.OK
}
