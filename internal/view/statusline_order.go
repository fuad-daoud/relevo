package view

// This file holds the statusline's finished-last ordering: the one projection
// both the text statusline and the JSON document read ranks running entries
// ahead of finished ones, so a finished chain never pushes a running one past
// the host's visible rows. It lives apart from statusline.go because that file
// is at the project's size cap.

// statusLineFinished reports whether a status row is finished: its Display is
// DONE. DONE bindings never reach the statusline, so finished rows are exactly
// done-chain rows.
func statusLineFinished(b BindingStatus) bool {
	return b.Display == "DONE"
}

// orderStatusLineBindings ranks finished units last, stably. A fork parent and
// the child rows threaded under it move as one unit, so a child that needs a
// human never parts from the parent it belongs under. Non-finished units keep
// their incoming order, which ApplyChains already attention-sorts; finished
// units keep theirs.
func orderStatusLineBindings(bindings []BindingStatus) []BindingStatus {
	present := make(map[string]bool, len(bindings))
	for _, b := range bindings {
		if b.Chain == nil || b.Chain.Parent == "" {
			present[b.Name] = true
		}
	}
	var tops []BindingStatus
	kids := make(map[string][]BindingStatus)
	var orphans []BindingStatus
	for _, b := range bindings {
		if b.Chain != nil && b.Chain.Parent != "" {
			if present[b.Chain.Parent] {
				parent := b.Chain.Parent
				kids[parent] = append(kids[parent], b)
			} else {
				orphans = append(orphans, b)
			}
			continue
		}
		tops = append(tops, b)
	}
	out := make([]BindingStatus, 0, len(bindings))
	var finished []BindingStatus
	flush := func(unit []BindingStatus, done bool) {
		if done {
			finished = append(finished, unit...)
		} else {
			out = append(out, unit...)
		}
	}
	for _, t := range tops {
		flush(append([]BindingStatus{t}, kids[t.Name]...), statusLineFinished(t))
	}
	flush(orphans, false)
	return append(out, finished...)
}
