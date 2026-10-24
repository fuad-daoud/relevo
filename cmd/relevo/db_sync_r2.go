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
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

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
	"input and beats " + relevosync.EnvR2Secret + ". The stdin flags read one standard\n" +
	"input, so passing any two of --token-stdin, --passphrase-stdin and\n" +
	"--r2-secret-stdin is refused. With none of the four, enable\n" +
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

// dbSyncEnableIntake refuses the one thing that cannot be resolved here: two
// secrets asked for on the same standard input.
//
// There is one stdin and four secrets that must not be concatenated on it, and
// a pipe carrying two would hand one program whichever value it happened to read
// first. Any two of them are refused. --token-stdin is not counted beside
// --join-stdin: the code carries the token, so that flag reads nothing.
//
// The refusal is raised before anything is dialed, so no process is started for
// an enable that cannot go ahead, and it names the flags that collided rather
// than saying "a secret".
func dbSyncEnableIntake(tokenStdin, passphraseStdin, joinStdin bool, r2 dbSyncR2Flags) error {
	var asked []string
	// A join's token comes from the code, so --token-stdin beside it reads
	// nothing and shares the pipe with nothing.
	if tokenStdin && !joinStdin {
		asked = append(asked, "--token-stdin")
	}
	if passphraseStdin {
		asked = append(asked, "--passphrase-stdin")
	}
	if *r2.secretStdin {
		asked = append(asked, "--r2-secret-stdin")
	}
	if joinStdin {
		asked = append(asked, "--join-stdin")
	}
	if len(asked) < 2 {
		return nil
	}
	return fail(codeUsage, "relevo db sync enable: %s and %s read one standard input; pass one",
		asked[0], asked[1])
}

// dbSyncR2SecretStdin is the reader --r2-secret-stdin reads. It is a var so a
// test can feed it.
var dbSyncR2SecretStdin io.Reader = os.Stdin

// dbSyncEnableR2Tail fills an enable's frame and returns the raw tail: the
// token, then the passphrase, then the bucket secret.
//
// The order is the split the owner reads back from the end, and it is fixed here
// so the two halves cannot disagree about where a secret starts. The bucket
// secret comes from --r2-secret-stdin, else RELEVO_R2_SECRET; with neither the
// secret is empty and the tail ends after the passphrase.
//
// The passphrase is placed rather than read here: this file owns the R2 half,
// and the passphrase's route is dbSyncPassphrase's, shared with unlock.
func dbSyncEnableR2Tail(token, passphrase []byte, r2 dbSyncR2Flags, opts *dbSyncVerbOptions) ([]byte, error) {
	secret := []byte(dbSyncGetenv(relevosync.EnvR2Secret))
	if *r2.secretStdin {
		read, err := io.ReadAll(dbSyncR2SecretStdin)
		if err != nil {
			return nil, failWrap(codeUsage, err, "relevo db sync enable --r2-secret-stdin")
		}
		if len(bytes.TrimSpace(read)) == 0 {
			return nil, fail(codeUsage, "relevo db sync enable --r2-secret-stdin: standard input carried no secret")
		}
		secret = read
	}
	secret = bytes.TrimSpace(secret)
	passphrase = bytes.TrimSpace(passphrase)
	opts.R2Endpoint, opts.R2Bucket, opts.R2KeyID = *r2.endpoint, *r2.bucket, *r2.keyID
	opts.R2SecretLen, opts.PassphraseLen = len(secret), len(passphrase)
	tail := make([]byte, 0, len(token)+len(passphrase)+len(secret))
	tail = append(tail, token...)
	tail = append(tail, passphrase...)
	return append(tail, secret...), nil
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
	if doc.Fenced, err = dbSyncFenced(shared); err != nil {
		return err
	}
	doc.IdentityTaken = relevosync.IdentityTaken(local)
	if pending := relevosync.AdoptPending(shared, local); pending != "" {
		doc.AdoptPending = pending
		doc.AdoptionPending = pending
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
	key, err := relevosync.KeyState(local)
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
		Key:          key,
		R2Configured: r2Ready,
		R2Endpoint:   r2Endpoint,
		R2Bucket:     r2Bucket,
		Latched:      state.Attention,
		LatchCause:   state.LatchCause,
		RebuildAt:    statusStamp(state.Rebuild.At),
		RebuildAside: state.Rebuild.Aside,
		Backlog:      state.Backlog,
		LastExport:   statusStamp(state.LastExport),
		LastImport:   statusStamp(state.LastImport),
		HeldOrigins:  state.Trouble.Held,
		Dropped:      state.Trouble.Dropped,
		Gaps:         state.Trouble.Gaps,
		LastAttempt:  attemptDoc(state.Attempt),
		Attempts:     dbSyncAttempts(local),
		Tables:       dbSyncTables(local),
	}
	if err := dbSyncStatusStorage(local, settings, &doc); err != nil {
		return dbSyncStatusDoc{}, err
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
	if pending := relevosync.AdoptPending(local); pending != "" {
		doc.AdoptPending = pending
		doc.AdoptionPending = pending
	}
	return doc, nil
}

// dbSyncStatusStorage populates bytes, storage, and quotas onto doc.
func dbSyncStatusStorage(local relevosync.Local, settings relevosync.Settings, doc *dbSyncStatusDoc) error {
	month, err := relevosync.ReadBytes(local, time.Now())
	if err != nil {
		return dbSyncClassify(err)
	}
	if !month.Zero() {
		doc.Bytes = &month
	}
	storage, err := relevosync.ReadStorage(local)
	if err != nil {
		return dbSyncClassify(err)
	}
	if !storage.At.IsZero() {
		doc.Storage = &storage
	}
	if doc.Bytes != nil || doc.Storage != nil {
		doc.QuotaTursoSync = settings.TursoSyncQuota()
		doc.QuotaTursoStorage, doc.QuotaR2Storage = settings.TursoStorageQuota(), settings.R2StorageQuota()
	}
	return nil
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

// dbSyncQuotasLine renders the month's traffic and the stored bytes against the
// plan quotas the document carries. A storage reading that was never taken reads
// as not measured, never as zero.
func dbSyncQuotasLine(doc dbSyncStatusDoc) string {
	var month relevosync.ByteCounters
	if doc.Bytes != nil {
		month = *doc.Bytes
	}
	var storage relevosync.Storage
	if doc.Storage != nil {
		storage = *doc.Storage
	}
	return relevosync.FormatQuotas(month, relevosync.Settings{
		QuotaTursoSync:    doc.QuotaTursoSync,
		QuotaTursoStorage: doc.QuotaTursoStorage,
		QuotaR2Storage:    doc.QuotaR2Storage,
	}, storage)
}
