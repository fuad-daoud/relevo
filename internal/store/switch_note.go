package store

// PollSwitchNotePrefix heads the note of the switch entry a poll writes when a
// server reports a candidate other than the recorded one. It names what the
// client observed, so readers that count a round's switches skip it once the
// server's own record of the round is in the log.
const PollSwitchNotePrefix = "switched on "

// SwitchReplacedClause ends a switch entry's note, naming the candidate the
// switch replaced. A server's copy of a binding may hold no pick for it, so this
// is the only record of what the rounds before the switch ran on.
const SwitchReplacedClause = "; replaced "
