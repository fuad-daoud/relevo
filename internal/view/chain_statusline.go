package view

// This file holds the statusline's chain row: the synthetic row that stands in
// for a chain in place of its members', and the split a fork parent carries in
// place of its children. It lives apart from statusline.go because that file is
// at the project's size cap.

// statusLineRowOfChain builds the statusline row that stands in for a
// chain: one entry per chain in place of its members' rows. The middle is the
// chain's plan segment, and the status column follows the chain -- NEEDS YOU
// while it waits on a human, DONE once it finished, ACTIVE while it works.
// The chain has no round and no candidate, so those cells stay empty and the
// clock keeps the "--" every row without a round shows.
//
// A fork's parent carries its split in the middle ("split 1/2") and, instead of
// a row per child, one extra row for the child that needs a human: a halted or
// NEEDS YOU child is never hidden behind its parent's summary, because that is
// the row a human has to act on.
func statusLineRowOfChain(b BindingStatus) StatusLineRow {
	display := b.Display
	status, tone := display, "quiet"
	switch display {
	case "ACTIVE":
		tone = "phase"
	case "NEEDS YOU":
		tone = "needs"
	}
	actor := actorOf(b, "chain")
	mid := "chain " + b.Name + " · " + ChainSegment(*b.Chain)
	if split := chainSplitText(*b.Chain); split != "" {
		mid += " · " + split
	}
	// The stranded member's name rides with the chain's own middle: it is the
	// only row the roll-up leaves for that member, so naming it here is what
	// keeps an uncollected payload from being invisible until someone opens
	// every member by hand.
	if pending := ChainPendingSegment(*b.Chain); pending != "" {
		mid += " · " + pending
	}
	return StatusLineRow{
		Name:     b.Name,
		Chain:    mid,
		Display:  display,
		NeedsYou: display == "NEEDS YOU",
		Actor:    actor,
		Clock:    "--",
		Status:   status,
		Tone:     tone,
		Reason:   b.Detail,
	}
}
