// Package trail holds the two bookkeeping lines the supervisor appends to a
// builder's stream once the builder exits: readers below and above the process
// model share them, so they live in this leaf.
package trail

// RusageTrailerPrefix is the prefix of the rusage line the supervisor appends
// inside a scoped spawn, right before the exit trailer. A payload tail skips
// lines carrying this prefix.
const RusageTrailerPrefix = "relevo-rusage:"

// ExitTrailer prefixes the last line of a builder's stream: the code the
// supervisor left follows it.
const ExitTrailer = "relevo-exit:"
