package synclog

import (
	"context"
	"time"
)

// LogTransport is where the per-origin log lives. It is the seam the exporter,
// importer and reconcile are written against.
// The one rule a transport owns is sequence assignment: a machine names no
// sequence numbers of its own. It hands over a batch and the transport numbers
// it, so a backup-restored file never reuses a number and two appends never
// hand out the same one.
type LogTransport interface {
	// Append writes one batch of this origin's entries as a single atomic
	// write, numbering them from one past the highest sequence this origin
	// already holds, and returns them numbered. Entries of another origin are
	// refused: an installation appends only for the rows it owns, and the
	// refusal is what stops two machines writing the same row.
	//
	// A transport only knows what origin an entry claims; which rows that origin owns is
	// a question about the reader's own file, so the importer answers it again
	// before it writes anything. An entry naming a row the reader resolves to
	// another installation is refused there even when it arrived through an
	// honest transport.
	//
	// A batch is one unit for a reader: the entries it wrote share a Batch, the
	// sequence number of the first of them, and a reader applies whole batches.
	Append(entries []Entry) ([]Entry, error)

	// Pull returns the entries of every origin other than this one that follow
	// the mark given for it, in sequence order and in whole batches. An origin
	// with no mark is read from the start of its log. A batch that begins at or
	// before the mark is not split: the reader either has the whole batch or
	// none of it.
	//
	// The sequence numbers and the origin an entry carries are the transport's
	// to hand out, and the reader does not take them on trust: an entry at or
	// below the mark it already holds is not applied again, and a mark is only
	// ever written forward from the entries actually applied, so a transport
	// behind or ahead of the mark can neither rewind rows nor skip entries.
	Pull(marks map[string]int) ([]Entry, error)

	// Head returns the latest entry per row of one origin: the row, the
	// sequence number of the entry that last wrote it and that entry's body
	// hash. This is the state reconcile compares against: a missing row was
	// never written and a different hash means changed contents.
	Head(origin string) ([]HeadRow, error)

	// Stats reports what the log holds, so a caller can tell an empty log from a
	// transport that is refusing to answer.
	Stats() (Stats, error)

	// PublishMarks records how far this reader has applied of each origin given,
	// as of at. The reader is the transport's own installation: a mark is what a
	// compaction floor is computed from, so naming another reader's would choose
	// what the log may drop on a machine that does not hold those entries.
	//
	// An origin's own mark is not published: a reader never needs one, and it
	// would only give the floor a row to be taught to ignore.
	PublishMarks(marks map[string]int, at time.Time) error

	// ReadMarks answers with every published mark, the floors the log has
	// recorded, and each origin's head and entry count: the three inputs a
	// compaction decides from. With no floors yet it answers an empty set
	// rather than an error, because nothing has compacted.
	ReadMarks() (MarkState, error)

	// PullRange returns entries of one origin with after < seq <= upto, in
	// sequence order. Unlike Pull it does not hand back whole batches: the
	// snapshot path reduces a tail-ending region to one entry per key, so a
	// batch cut by a page changes nothing.
	// A transport may answer short; the caller asks again from the last
	// sequence it was given until an answer is empty.
	PullRange(origin string, after, upto int) ([]Entry, error)

	// CompactOwn drops this installation's own superseded entries up to floor and
	// records the floor, as one write. The origin is the transport's own, as in
	// PublishMarks: only the machine that wrote a log may drop from it.
	CompactOwn(floor int) (CompactResult, error)

	// KeyMeta reads the passphrase material the remote holds, answering Present
	// false where the log holds none. It precedes any signing: a machine about
	// to sign must first learn what the log was set up with.
	KeyMeta(ctx context.Context) (KeyMeta, error)

	// KeyInit writes that material where the log holds none and answers with
	// what it holds afterwards. The answer rather than the request is what a
	// caller acts on: two machines racing the first one both learn the winner's
	// salt and each compares its own check against it.
	KeyInit(ctx context.Context, key KeyMeta) (KeyMeta, error)

	// KeyRekey rotates the log's key to the epoch the spec names: the new
	// material, the replaced key sealed for the machines still holding it,
	// and every origin's cut, all in one transaction refusing a remote whose
	// epoch or heads moved under the caller. It answers with what the remote
	// holds afterwards, another machine's when this one lost the race.
	KeyRekey(ctx context.Context, spec KeyRekeySpec) (KeyMeta, error)

	// PublishEpochCut extends this installation's own cut to the cut given,
	// which must not name a sequence the log does not hold and must not move
	// the stored one backwards. Only the machine that wrote a log may extend what its
	// history is fenced at, and a cut extension is how a machine vouches for
	// the tail it appended before learning of the rekey.
	//
	// The tag travels with the cut; the worker side checks only that the
	// sequence is forward and within the log, because which key the tag
	// verifies under is about the reader, not the writer.
	PublishEpochCut(ctx context.Context, cut EpochCut) error

	// ClaimIdentity records that a machine is adopting origin's identity under
	// token, where nobody holds a live claim: the row is written when absent,
	// when it is the caller's own token, or when it is older than ClaimTTL at
	// the time given. It answers with the claim the log holds afterwards, read back
	// after the write, and the caller compares the token rather than trusting its
	// own write stuck: two machines racing one claim can each read their own token
	// back before the later write lands, so the claim is read again later and a
	// mismatch then is the loss.
	//
	// An empty token writes nothing and only reads the claim, which is how a
	// steady attempt checks the identity is still its own. An origin with
	// no claim answers with an empty winner.
	ClaimIdentity(ctx context.Context, origin, token string, at time.Time) (winner string, claimedAt time.Time, err error)
}

// ClaimTTL is how long a claim on an identity stands against another machine's:
// the same day AdoptionRefusal gives an identity's last activity.
const ClaimTTL = 24 * time.Hour

// KeyMeta is the passphrase material a transport holds: the KDF salt, the name
// of the derivation, and the check value derived from the key.
//
// The salt and the check are not secret -- the remote holds them so another
// machine can derive the same key and compare -- but everything here arrived
// from a machine this one does not control, so a caller treats all of it as a
// claim to check rather than as a fact.
type KeyMeta struct {
	Salt []byte
	KDF  string
	// Check is the value a wrong passphrase does not reproduce.
	Check []byte
	// Present says whether a key has been set up at all. It is not read from
	// the salt being empty, because a remote that holds a check without a salt
	// is a different state from one that holds neither.
	Present bool
	// Epoch is the epoch this material's key is. A remote that holds no
	// key_epoch row is at epoch 1, and the transport hands every reader that 1
	// rather than a zero each caller would have to default itself.
	Epoch int
	// Wrapped is the older epochs' keys the remote holds sealed, keyed by the
	// epoch each one is. It is nil on a remote that holds none, which is every
	// remote before its first rekey.
	Wrapped map[int][]byte
}

// EpochCut is one origin's cut: the epoch entries above the cut must verify
// under, the sequence the cut sits at, and the tag binding origin, epoch and
// sequence to the epoch's key.
type EpochCut struct {
	Epoch int
	// Seq is the highest sequence the cut vouches for as history.
	Seq int
	// Tag is the HMAC that binds the three to the epoch's key.
	Tag []byte
}

// KeyRekeySpec is what one rekey writes: the epoch it is rotating from, the
// material the remote is to hold, and the cut each origin's history is fenced
// at.
type KeyRekeySpec struct {
	// From is the epoch the caller read the remote at. The rekey is a
	// compare-and-set against it, so a remote whose epoch has moved under the
	// caller -- another machine rekeyed first -- refuses and changes nothing.
	From int
	// Key is the material the remote holds afterwards: a fresh salt, the
	// derivation, the new key's check, the epoch it names, and the key it
	// replaced sealed for the machines still holding it.
	Key KeyMeta
	// Cuts is one cut per origin in the log, each at that origin's head as the
	// caller observed it.
	Cuts map[string]EpochCut
}

// CompactResult is what one compaction did: how many entries it dropped and the
// floor it now records, never below the previous one.
type CompactResult struct {
	Dropped int
	Floor   int
}

// MarkState is what a transport holds about compaction: the marks readers
// published, and the log's own size per origin.
//
// The maps are nil rather than empty when there is nothing to report, so an
// origin that has compacted nothing and one whose floor is at the start of its
// log stay apart -- the two mean different things to whoever reads them.
type MarkState struct {
	// Marks are the published positions, one per reader per origin.
	Marks []Mark
	// Floors is what each origin has already been compacted to.
	Floors map[string]int
	// Heads is each origin's highest sequence in the log.
	Heads map[string]int
	// Entries is how many entries the log holds for each origin.
	Entries map[string]int
	// Sizes is each origin's log size in bytes: the bodies plus a fixed
	// overhead per row.
	Sizes map[string]int64
	// SignedFrom is the first sequence of each origin that was signed. An
	// origin that has never signed is absent from the map, which is how a
	// reader tells that from an origin whose very first entry was signed.
	SignedFrom map[string]int
	// SignedTags is the tag stored beside each SignedFrom entry; an origin
	// whose stored boundary has none, or none that parses, maps to nil.
	SignedTags map[string][]byte
	// EpochCuts is each origin's cut as the log holds it, one row per origin
	// a rekey wrote. The tag is unverified here -- the reader's job -- and a
	// row this transport cannot parse comes back with a zero epoch and no
	// tag, the shape nothing verifies under.
	EpochCuts map[string]EpochCut
}

// Boundary is the first signed sequence an exporter predicts for its origin and
// the tag binding it to the key. The zero value carries none.
type Boundary struct {
	Seq int
	Tag []byte
}

type BoundaryAppender interface {
	AppendSigned(entries []Entry, boundary Boundary) ([]Entry, error)
}

// TrafficRow is one installation's month of sync traffic as it publishes it:
// the Turso bytes it pushed and pulled in the named UTC month, and when it
// read its own counters.
type TrafficRow struct {
	// Month is the UTC month the counts cover, spelled "2006-01".
	Month string
	// Push and Pull are the bytes this machine sent to and read from Turso.
	Push int64
	Pull int64
	// At is when the publisher read its counters.
	At time.Time
}

// TrafficPublisher carries this installation's traffic row to the log and answers
// with every origin's row the log holds. It stays optional rather than a
// LogTransport method so one addition does not churn every fake; a caller asks
// for it with a type assertion and skips the publish elsewhere.
type TrafficPublisher interface {
	// PublishTraffic writes this installation's own row -- the origin is the
	// transport's own, the rule PublishMarks states -- and answers with every
	// origin's row as the log holds it after the write.
	PublishTraffic(row TrafficRow) (map[string]TrafficRow, error)
}

// LogRowOverhead is the bytes a log row costs beside its body, for the columns
// the body does not carry.
const LogRowOverhead = 96

// HeadRow is one row of an origin's `head`: the materialized latest state of a
// single shared row, as the log holds it.
type HeadRow struct {
	Table string
	// PK is the row's primary key as the json_array text the outbox records.
	PK string
	// Seq is the sequence number of the entry that last wrote the row.
	Seq int
	// Hash is that entry's body hash, what a locally read row is compared against.
	Hash string
}

// Stats is what a transport reports about the log it holds.
type Stats struct {
	// Entries is how many entries the log holds across every origin.
	Entries int
	// Origins is how many distinct origins have appended to it.
	Origins int
	// Seq is the highest sequence number the log holds, across origins: one
	// origin owns each number, so this is the largest rather than a count.
	Seq int
	// TursoSent and TursoReceived are the network bytes pushed and pulled over one
	// transport's life, and R2Put and R2Get the body bytes sent to and taken from
	// the bucket. All four are cumulative and are what a month of sync costs; zero
	// means the transport moved nothing, which tells a fresh one from one not reporting.
	TursoSent     int64
	TursoReceived int64
	R2Put         int64
	R2Get         int64
}
