package syncworker

// The batched blob verbs. A body never crosses the pipe, so a batch names its
// bodies the same way the single verbs do: one item per key, each carrying the
// staging path the daemon wrote or wants written. One key's failure rides
// inside the reply rather than refusing the whole call, because it is no
// verdict on the rest of its batch.

// VerbPutBlobs uploads one batch of staged bodies under their keys.
const VerbPutBlobs Verb = "put_blobs"

// VerbGetBlobs fetches one batch of bodies out of R2 into their staged files.
const VerbGetBlobs Verb = "get_blobs"

// BlobItem is one body a batch verb moves: the object key and the staging
// path it is read from or written to. Max is the most bytes a get may write,
// the length the ref declares; a put ignores it, since the staged file's own
// digest decides what is published.
type BlobItem struct {
	// Key names the object, as "<origin>/<sha256>".
	Key string `json:"key,omitempty"`
	// Staging is the absolute local path the body is read from or written to.
	Staging string `json:"staging,omitempty"`
	// Max is the most bytes get_blobs may write for this key.
	Max int64 `json:"max,omitempty"`
}

// BlobResult is what one key of a batch came to: the bytes it moved, or the
// reason it did not. A refusal keeps the class the single verbs answer with,
// so the daemon maps it the same way without reading the text.
type BlobResult struct {
	// Key is the item's, echoed so a reply read out of order still names which
	// key each result belongs to.
	Key string `json:"key"`
	// Bytes is what this key moved. A skipped put moved nothing, so it carries
	// no bytes: the object was already there.
	Bytes int64 `json:"bytes,omitempty"`
	// Skipped says a put found the object already in the store and sent
	// nothing: the object is there, so the entry may be appended.
	Skipped bool `json:"skipped,omitempty"`
	// Error is the refusal's own words, and empty on success.
	Error string `json:"error,omitempty"`
	// Code is the refusal's class, empty on success and on a refusal a retry
	// can still get past.
	Code RefusalCode `json:"code,omitempty"`
}
