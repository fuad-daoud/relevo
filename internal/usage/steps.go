package usage

import (
	"bytes"
	"encoding/json"
	"math"
	"sort"
	"time"
)

// StepStats is what one round's builder stream says about its model steps. Zero
// means the harness's stream does not show the figure.
type StepStats struct {
	Steps            int
	ToolCalls        int
	StepP50MS        int64
	FirstOutputP50MS int64
}

// StreamSteps counts the model steps and tool calls in a round's builder stream and
// takes the median step duration and time to first output from it. It is pure and
// never fails. Its rules key on event names unique to each harness, so a stream
// holding a second run after a mid-round switch is read as one.
func StreamSteps(harness string, stream []byte) StepStats {
	var st StepStats
	var stepSamples, firstSamples []int64
	switch harness {
	case "opencode":
		opencodeSteps(stream, &st, &stepSamples, &firstSamples)
	case "claude":
		claudeSteps(stream, &st, &stepSamples, &firstSamples)
	case "agy":
		agySteps(stream, &st, &stepSamples)
	case "codex":
		codexSteps(stream, &st)
	default:
		return StepStats{}
	}
	st.StepP50MS = lowerMedian(stepSamples)
	st.FirstOutputP50MS = lowerMedian(firstSamples)
	return st
}

// eachLine calls fn for every line whose first non-space byte is "{".
func eachLine(stream []byte, fn func(line []byte)) {
	for _, line := range bytes.Split(stream, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		fn(line)
	}
}

// lowerMedian returns the element at (n-1)/2: with an even count, the earlier of
// the two middles. No samples is 0.
func lowerMedian(samples []int64) int64 {
	if len(samples) == 0 {
		return 0
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	return samples[(len(samples)-1)/2]
}

// opencodeStepEvent covers the fields StreamSteps reads from an opencode line.
type opencodeStepEvent struct {
	Type      string `json:"type"`
	Timestamp int64  `json:"timestamp"`
	Part      *struct {
		Time *struct {
			Start int64 `json:"start"`
		} `json:"time"`
		State *struct {
			Time *struct {
				Start int64 `json:"start"`
			} `json:"time"`
		} `json:"state"`
	} `json:"part"`
}

// opencodeSteps: a step is a step_start, its duration the step_finish that closes
// it, its first output the first text, reasoning or tool_use part it produced.
func opencodeSteps(stream []byte, st *StepStats, stepSamples, firstSamples *[]int64) {
	var stepStart int64
	var open, firstSeen bool
	eachLine(stream, func(line []byte) {
		var ev opencodeStepEvent
		if json.Unmarshal(line, &ev) != nil {
			return
		}
		switch ev.Type {
		case "step_start":
			st.Steps++
			stepStart, open, firstSeen = ev.Timestamp, true, false
		case "text", "reasoning", "tool_use":
			if ev.Type == "tool_use" {
				st.ToolCalls++
			}
			if !open || firstSeen {
				return
			}
			if d := opencodeOutputTime(ev) - stepStart; d >= 0 {
				*firstSamples = append(*firstSamples, d)
			}
			firstSeen = true
		case "step_finish":
			if !open {
				return
			}
			if d := ev.Timestamp - stepStart; d >= 0 {
				*stepSamples = append(*stepSamples, d)
			}
		}
	})
}

// opencodeOutputTime: the part's own start, else a tool part's state start, else the
// line's timestamp.
func opencodeOutputTime(ev opencodeStepEvent) int64 {
	switch {
	case ev.Part != nil && ev.Part.Time != nil && ev.Part.Time.Start > 0:
		return ev.Part.Time.Start
	case ev.Part != nil && ev.Part.State != nil && ev.Part.State.Time != nil && ev.Part.State.Time.Start > 0:
		return ev.Part.State.Time.Start
	}
	return ev.Timestamp
}

// claudeStepEvent covers the fields StreamSteps reads from a claude line.
type claudeStepEvent struct {
	Type            string  `json:"type"`
	Subtype         string  `json:"subtype"`
	Timestamp       string  `json:"timestamp"`
	ParentToolUseID *string `json:"parent_tool_use_id"`
	Message         *struct {
		ID      string `json:"id"`
		Content []struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		} `json:"content"`
	} `json:"message"`
}

type claudeStep struct {
	start       int64 // the lastUserTS at the id's first assistant event
	hasStart    bool
	lastSeen    int64
	hasLastSeen bool
}

// claudeSteps: a step is a distinct message.id on the main thread, a tool call a
// distinct tool_use block id, and a step's duration its last assistant event minus
// the user event preceding it. Sub-agent traffic is excluded.
func claudeSteps(stream []byte, st *StepStats, stepSamples, firstSamples *[]int64) {
	s := &claudeStepState{
		st: st, stepSamples: stepSamples, firstSamples: firstSamples,
		blocks: map[string]bool{}, steps: map[string]*claudeStep{},
	}
	eachLine(stream, s.feed)
	for _, id := range s.order {
		s.close(s.steps[id])
	}
}

type claudeStepState struct {
	st           *StepStats
	stepSamples  *[]int64
	firstSamples *[]int64
	lastUser     int64
	hasUser      bool
	blocks       map[string]bool
	steps        map[string]*claudeStep
	order        []string
}

func (s *claudeStepState) feed(line []byte) {
	var ev claudeStepEvent
	if json.Unmarshal(line, &ev) != nil {
		return
	}
	switch ev.Type {
	case "system":
		// The init event stands in for a user event until one arrives.
		if ev.Subtype == "init" && !s.hasUser {
			s.lastUser, s.hasUser = claudeTS(ev.Timestamp)
		}
	case "assistant", "user":
		s.userOrAssistant(ev)
	}
}

func (s *claudeStepState) userOrAssistant(ev claudeStepEvent) {
	if ev.ParentToolUseID != nil {
		return
	}
	ts, hasTS := claudeTS(ev.Timestamp)
	if ev.Type == "user" {
		if hasTS {
			s.lastUser, s.hasUser = ts, true
		}
		return
	}
	if ev.Message == nil {
		return
	}
	for _, b := range ev.Message.Content {
		if b.Type == "tool_use" && !s.blocks[b.ID] {
			s.blocks[b.ID] = true
			s.st.ToolCalls++
		}
	}
	s.message(ev.Message.ID, ts, hasTS)
}

func (s *claudeStepState) message(id string, ts int64, hasTS bool) {
	step, ok := s.steps[id]
	if !ok {
		step = &claudeStep{start: s.lastUser, hasStart: s.hasUser}
		s.steps[id] = step
		s.order = append(s.order, id)
		s.st.Steps++
		if hasTS && s.hasUser {
			if d := ts - s.lastUser; d >= 0 {
				*s.firstSamples = append(*s.firstSamples, d)
			}
		}
	}
	if hasTS {
		step.lastSeen, step.hasLastSeen = ts, true
	}
}

func (s *claudeStepState) close(step *claudeStep) {
	if !step.hasStart || !step.hasLastSeen {
		return
	}
	if d := step.lastSeen - step.start; d >= 0 {
		*s.stepSamples = append(*s.stepSamples, d)
	}
}

// claudeTS parses an RFC3339 timestamp to epoch milliseconds.
func claudeTS(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0, false
	}
	return t.UnixMilli(), true
}

// agyStepEvent covers the fields StreamSteps reads from an agy line.
type agyStepEvent struct {
	Event      string `json:"event"`
	StepUpdate *struct {
		StepType        string  `json:"step_type"`
		State           string  `json:"state"`
		StepIndex       int     `json:"step_index"`
		DurationSeconds float64 `json:"duration_seconds"`
	} `json:"step_update"`
}

// agySteps: a step is a distinct agent_response step_index that reached DONE, its
// duration the harness's own duration_seconds; a tool call is a distinct tool
// step_index that reached ACTIVE. agy carries no wall-clock timestamps.
func agySteps(stream []byte, st *StepStats, stepSamples *[]int64) {
	seenSteps := map[int]bool{}
	seenTools := map[int]bool{}
	eachLine(stream, func(line []byte) {
		var ev agyStepEvent
		if json.Unmarshal(line, &ev) != nil || ev.Event != "step_update" || ev.StepUpdate == nil {
			return
		}
		switch {
		case ev.StepUpdate.StepType == "agent_response" && ev.StepUpdate.State == "DONE":
			if seenSteps[ev.StepUpdate.StepIndex] {
				return
			}
			seenSteps[ev.StepUpdate.StepIndex] = true
			st.Steps++
			if ms := int64(math.Round(ev.StepUpdate.DurationSeconds * 1000)); ms > 0 {
				*stepSamples = append(*stepSamples, ms)
			}
		case ev.StepUpdate.StepType == "tool" && ev.StepUpdate.State == "ACTIVE":
			if seenTools[ev.StepUpdate.StepIndex] {
				return
			}
			seenTools[ev.StepUpdate.StepIndex] = true
			st.ToolCalls++
		}
	})
}

// codexStepEvent covers the fields StreamSteps reads from a codex line.
type codexStepEvent struct {
	Type string `json:"type"`
	Item *struct {
		Type string `json:"type"`
	} `json:"item"`
}

// codexSteps: codex exposes no model steps, so Steps stays 0; an item.completed that
// is a command, a file change or a sub-agent or mcp tool call is one tool call.
func codexSteps(stream []byte, st *StepStats) {
	eachLine(stream, func(line []byte) {
		var ev codexStepEvent
		if json.Unmarshal(line, &ev) != nil || ev.Type != "item.completed" || ev.Item == nil {
			return
		}
		switch ev.Item.Type {
		case "command_execution", "file_change", "collab_tool_call", "mcp_tool_call":
			st.ToolCalls++
		}
	})
}
