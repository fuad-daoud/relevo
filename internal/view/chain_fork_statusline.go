package view

import "fmt"

// This file holds the statusline's fork projection: the split a parent row
// carries in place of its children, and the rule that decides which child still
// gets a row of its own. It lives apart from statusline.go because that file is
// at the project's size cap.

// chainSplitText is a fork parent's collapsed middle: "split 1/2", how many of
// its children are done. A chain with no children returns "", so its middle is
// exactly the text it had before forks existed.
func chainSplitText(f ChainFacts) string {
	if len(f.Children) == 0 {
		return ""
	}
	return fmt.Sprintf("split %d/%d", f.ChildrenDone, len(f.Children))
}

// ChainChildRow is the statusline row a fork's child adds under its parent: a
// row is emitted only for a child that halted or that reads NEEDS YOU, so the
// collapsed parent never hides the one child a human must act on. A running or
// done child returns false and stays inside the parent's "split 1/2".
//
// This is the same projection the Go statusline and the status --line --json
// document read, so neither consumer can expand a child the other hides.
func ChainChildRow(b BindingStatus) (StatusLineRow, bool) {
	if b.Chain == nil || b.Chain.Parent == "" {
		return StatusLineRow{}, false
	}
	if b.Chain.Status != "halted" && b.Chain.Status != "stopped" && b.Display != "NEEDS YOU" {
		return StatusLineRow{}, false
	}
	display := b.Display
	if display == "" {
		display = "NEEDS YOU"
	}
	return StatusLineRow{
		Name:     b.Name,
		Chain:    "child " + b.Name + " · " + ChainSegment(*b.Chain),
		Display:  display,
		NeedsYou: true,
		Actor:    actorOf(b, "chain"),
		Clock:    "--",
		Status:   display,
		Tone:     "needs",
		Reason:   b.Detail,
	}, true
}
