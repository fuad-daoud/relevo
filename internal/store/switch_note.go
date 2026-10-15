package store

// PollSwitchNotePrefix heads the note of the switch entry a poll writes when a
// server reports a candidate other than the recorded one. It names what the
// client observed, so readers that count a round's switches skip it once the
// server's own record of the round is in the log.
const PollSwitchNotePrefix = "switched on "
