package main

// The R2 half of the sync status surface, kept beside the verb rather than in
// it: db_sync.go is the whole sync CLI and this is one field of one document.
//
// The rule the whole file exists to hold is about what status may say. The
// endpoint and the bucket are named because they are what a reader needs to tell
// this machine's bucket from another's. The key id and the secret are not, in
// any shape, because this document is printed on request, pasted into an issue
// and read aloud, and a credential has no reason in any of those. Reading them
// here and copying only the two is what keeps a field added later from leaking
// by default.

import (
	"flag"
	"fmt"
	"os"

	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// The R2 fields of the status document. They live here rather than in db_sync.go
// so the fields, the reader that fills them and the renderer that prints them
// are one file: a credential could only leak by someone adding a field beside
// the reader, and there is now nowhere else to add one.
//
// R2Configured says whether this machine holds a complete set. R2Endpoint and
// R2Bucket describe where bodies would go, which is what a reader needs to tell
// this machine's bucket from another's. There is deliberately no field for the
// key id or the secret: this document is printed on request and read aloud, and
// a credential has no reason to be in either.
type dbSyncR2Status struct {
	R2Configured bool   `json:"r2_configured,omitempty"`
	R2Endpoint   string `json:"r2_endpoint,omitempty"`
	R2Bucket     string `json:"r2_bucket,omitempty"`
}

// dbSyncR2Usage is the R2 half of the enable line the usage text prints: the
// three flags that name where bodies go, and the one that carries the secret.
// It is its own constant so the sentence saying where the secret comes from can
// sit beside the flags it belongs to.
const dbSyncR2Usage = "       relevo db sync enable [--r2-endpoint URL] [--r2-bucket NAME]\n" +
	"       relevo db sync enable [--r2-key-id ID] [--r2-secret-stdin]\n"

// syncR2Status reads the machine's bucket credentials and returns the two halves
// status may show, plus whether the set is complete.
//
// A machine holding three of the four is reported as unconfigured rather than as
// half-configured: two of them is not somewhere a body can go, and the useful
// thing to tell a reader is that sync cannot use bodies here yet.
func syncR2Status(local relevosync.Local) (configured bool, endpoint, bucket string, err error) {
	r2, err := relevosync.ReadR2(local)
	if err != nil {
		return false, "", "", err
	}
	if !r2.Complete() {
		return false, "", "", nil
	}
	return true, r2.Endpoint, r2.Bucket, nil
}

// dbSyncR2UsageText is what the usage says about the bucket credentials: where
// the secret comes from, and the same rule the token follows applied to it.
const dbSyncR2UsageText = "--r2-endpoint, --r2-bucket and --r2-key-id name the bucket bodies\n" +
	"move through; --r2-secret-stdin reads the " + relevosync.SecretR2Secret +
	" from standard\n" +
	"input and beats " + relevosync.EnvR2Secret + ". The two stdin flags read one\n" +
	"standard input, so passing both is refused. With none of the four, enable\n" +
	"refuses and names them; what is stored already stands in for what is not\n" +
	"passed, so pointing this machine at another bucket keeps its access key.\n"

// dbSyncR2Flags are the pointers the R2 flags of `db sync enable` parse into.
// They are one struct so the flags, the refusals they meet and the fields they
// fill are a single thing to read, rather than four loose pointers spread over
// two files that have to be kept in step by hand.
type dbSyncR2Flags struct {
	endpoint    *string
	bucket      *string
	keyID       *string
	secretStdin *bool
}

// installR2Flags declares the R2 flags on fs.
func installR2Flags(fs *flag.FlagSet, v *dbSyncR2Flags) {
	v.endpoint = fs.String("r2-endpoint", "", "the R2 account endpoint, stored in the machine-local secrets")
	v.bucket = fs.String("r2-bucket", "", "the R2 bucket bodies are stored under")
	v.keyID = fs.String("r2-key-id", "", "the R2 access key that signs each request")
	v.secretStdin = fs.Bool("r2-secret-stdin", false, "read the r2.secret from standard input")
}

// dbSyncEnableR2 refuses the one thing that cannot be resolved here: two secrets
// asked for on the same standard input.
//
// There is one stdin and two secrets that must not be concatenated on it, and a
// pipe carrying both would hand one program whichever value it happened to read
// first. Refusing is the only safe reading, and it is refused before anything is
// dialed so no process is started for an enable that cannot go ahead.
func dbSyncEnableR2(tokenStdin bool, r2 dbSyncR2Flags) error {
	if tokenStdin && *r2.secretStdin {
		return fail(codeUsage, "relevo db sync enable: --token-stdin and --r2-secret-stdin read one standard input; pass one")
	}
	return nil
}

// cmdDBSyncStatus answers what this machine is set to be. It reads the local
// marks and nothing else, so it answers with the network blackholed and no
// handle open.
//
// It reaches them through the owner (openDBSyncStatus) and reads them itself
// rather than asking for a verb, and that is the whole of what S7 does not move:
// status is a description of the machine, so it stays a read. What it prints is
// a function of the document alone.
func cmdDBSyncStatus(args []string) error {
	fs := flag.NewFlagSet("db sync status", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := dbSyncStatusFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fail(codeUsage, "relevo db sync status takes no arguments, got %d", fs.NArg())
	}
	shared, local, err := openDBSyncStatus()
	if err != nil {
		return err
	}
	defer func() { _ = shared.Close() }()

	doc, err := dbSyncStatusRead(local)
	if err != nil {
		return err
	}
	if *v.asJSON {
		return printDoc(doc)
	}
	fmt.Print(dbSyncStatusLine(doc))
	return nil
}

// dbSyncStatusRead builds the status document from the machine-local rows. Every
// read here is of the local file, which is what lets status answer with the
// network blackholed and no worker open.
func dbSyncStatusRead(local relevosync.Local) (dbSyncStatusDoc, error) {
	settings, err := relevosync.ReadSettings(local)
	if err != nil {
		return dbSyncStatusDoc{}, failWrap(codeConfigInvalid, err, "relevo db sync status")
	}
	_, hasToken, err := relevosync.ReadToken(local)
	if err != nil {
		return dbSyncStatusDoc{}, failWrap(codeConfigInvalid, err, "relevo db sync status")
	}
	// The bucket half is read here rather than asked of the worker, so status
	// stays a read of the local file with the network blackholed.
	r2Ready, r2Endpoint, r2Bucket, err := syncR2Status(local)
	if err != nil {
		return dbSyncStatusDoc{}, failWrap(codeConfigInvalid, err, "relevo db sync status")
	}
	state, err := relevosync.ReadState(local)
	if err != nil {
		return dbSyncStatusDoc{}, dbSyncClassify(err)
	}

	doc := dbSyncStatusDoc{
		Enabled:      state.Enabled,
		RemoteURL:    settings.RemoteURL,
		Namespace:    settings.Namespace,
		TokenPresent: hasToken,
		R2Configured: r2Ready,
		R2Endpoint:   r2Endpoint,
		R2Bucket:     r2Bucket,
		Latched:      state.Attention,
		LatchCause:   state.LatchCause,
		Backlog:      state.Backlog,
		LastExport:   statusStamp(state.LastExport),
		LastImport:   statusStamp(state.LastImport),
		HeldOrigins:  state.Trouble.Held,
		Dropped:      state.Trouble.Dropped,
		Gaps:         state.Trouble.Gaps,
		LastAttempt:  attemptDoc(state.Attempt),
	}
	// A join in progress is a read of the machine-local marker, so status can
	// show one with the network blackholed and no worker open.
	progress, joining, err := relevosync.ReadJoin(local)
	if err != nil {
		return dbSyncStatusDoc{}, dbSyncClassify(err)
	}
	if joining {
		doc.Joining, doc.JoinSince = true, statusStamp(progress.At)
	}
	return doc, nil
}

// dbSyncR2Line is where bodies would go, or empty on a machine that has no
// bucket. An unconfigured machine gets no clause at all rather than one that
// says "not configured": bodies in a bucket are opt-in, and a machine that has
// not turned them on is in the ordinary state, not in one that needs reporting.
// The reader learns it is off from the absence, as with every other unmeasured
// fact on this line.
func dbSyncR2Line(doc dbSyncStatusDoc) string {
	if !doc.R2Configured {
		return ""
	}
	return doc.R2Endpoint + "/" + doc.R2Bucket
}
