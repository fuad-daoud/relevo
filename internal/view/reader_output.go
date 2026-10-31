package view

import (
	"bytes"
	"os"
	"strings"

	"github.com/fuad-daoud/relevo/internal/reporttail"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/text"
	"github.com/fuad-daoud/relevo/internal/transcript"
)

// ReaderOutputEmpty reports whether the runner's output file is empty for the
// close's purposes: zero bytes, or content that is only whitespace once
// trimmed. A file a runner created and then wrote nothing into is not an
// artifact, and treating it as one leaves the round with a blank output and no
// say so. The read error is reported rather than guessed at, so a file that
// cannot be read is never silently overwritten by the fallback.
func ReaderOutputEmpty(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	return len(bytes.TrimSpace(data)) == 0, nil
}

// ReaderFallbackNote is the log-note token a close records when it saved the
// stream because the runner's output file was missing or empty, and the token
// ReaderFallbackHeader looks for when it marks the same output. Both surfaces
// read this one constant, so the note and the header cannot disagree about
// which output was salvaged.
const ReaderFallbackNote = "output-fallback"

// ReaderFallbackHeaderLine is the line `show --output` puts above a salvaged
// output, so the MasterMind reading it learns the file is not the runner's own.
const ReaderFallbackHeaderLine = "note: the runner left no output file, so this is its last message, saved by the close."

// WithFallbackHeader puts the fallback line above a salvaged output, so the
// reader of it is never left believing the runner wrote the file.
func WithFallbackHeader(entries []store.LogEntry, round int, text string) string {
	if head := ReaderFallbackHeader(entries, round); head != "" {
		return head + "\n" + text
	}
	return text
}

// ReaderFallbackHeader returns ReaderFallbackHeaderLine when name's round closed
// with the fallback marked, else "". entries is the binding's whole log; the
// search is from the newest entry back, so the newest marking of the round wins.
func ReaderFallbackHeader(entries []store.LogEntry, round int) string {
	for i := len(entries) - 1; i >= 0; i-- {
		if e := entries[i]; e.Round == round && strings.Contains(e.Note, ReaderFallbackNote) {
			return ReaderFallbackHeaderLine
		}
	}
	return ""
}

// ReaderOutputText is the message a reader's output file is written from. A
// chain reviewer or security member's block-carrying message is returned with
// its block stripped: relevo rewrites the file the mastermind reads, and the
// parse already read the block. Any other reader prefers the last message that
// carried a block, block kept so the close can parse and strip it, falling back
// to the stream's last assistant text when no message carried one.
func ReaderOutputText(kind string, stream []byte, chainReader bool) string {
	withBlock := transcript.LastWithBlock(kind, stream)
	if chainReader {
		if withBlock != "" {
			return string(reporttail.StripTail([]byte(withBlock)))
		}
		return transcript.FinalText(kind, stream)
	}
	if withBlock != "" {
		return withBlock
	}
	return transcript.FinalText(kind, stream)
}

// ReaderOutputPresent reports whether path is a regular file the runner wrote
// with content in it, and whether that file is empty. A missing path, a symlink,
// a directory or a fifo is not present and not empty: whether it is a refusal
// is the close's decision, not this one's, so nothing here follows it.
func ReaderOutputPresent(path string) (present, empty bool) {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return false, false
	}
	if empty, err = ReaderOutputEmpty(path); err != nil {
		return true, false
	}
	return true, empty
}

// LastStreamKind is the harness kind of the round's last stream segment: the
// kind of the process that last wrote, which a mid-round switch changes. The
// endpoint's own Kind is the fallback for a round that recorded no segment.
func LastStreamKind(b store.Binding) string {
	kinds := make([]string, 0, len(b.Builder.StreamSegments))
	for _, seg := range b.Builder.StreamSegments {
		kinds = append(kinds, seg.Kind)
	}
	return text.LastStreamKind(b.Builder.Kind, kinds)
}
