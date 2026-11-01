package main

// The R2 half of the sync status surface, kept beside the verb rather than in
// it. Status may name the endpoint and the bucket but never the key id or the
// secret: the document is printed on request and read aloud, so a credential has
// no reason in it. Reading the set here and copying only the two keeps a field
// added later from leaking by default.

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// dbSyncR2Usage is the R2 half of the enable line the usage text prints. It is
// its own constant so the sentence saying where the secret comes from sits
// beside the flags it belongs to.
const dbSyncR2Usage = "       relevo db sync enable [--r2-endpoint URL] [--r2-bucket NAME]\n" +
	"       relevo db sync enable [--r2-key-id ID] [--r2-secret-stdin]\n"

// syncR2Status reads the machine's bucket credentials and returns what status
// may show, plus whether the set is complete. A machine holding three of the four
// is reported as unconfigured: two of them is nowhere a body can go.
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

// dbSyncR2Flags holds the pointers the R2 flags of `db sync enable` parse into:
// one struct so the flags and the fields they fill are read as one thing rather
// than four loose pointers kept in step by hand.
type dbSyncR2Flags struct {
	endpoint    *string
	bucket      *string
	keyID       *string
	secretStdin *bool
}

func installR2Flags(fs *flag.FlagSet, v *dbSyncR2Flags) {
	v.endpoint = fs.String("r2-endpoint", "", "the R2 account endpoint, stored in the machine-local secrets")
	v.bucket = fs.String("r2-bucket", "", "the R2 bucket bodies are stored under")
	v.keyID = fs.String("r2-key-id", "", "the R2 access key that signs each request")
	v.secretStdin = fs.Bool("r2-secret-stdin", false, "read the r2.secret from standard input")
}

// dbSyncEnableIntake refuses the one thing that cannot be resolved here: two
// secrets asked for on the same standard input, where a pipe carrying two
// would hand one program whichever value it read first. --token-stdin is not
// counted beside --join-stdin: the code carries the token, so that flag reads
// nothing. The refusal is raised before anything is dialed, naming the flags
// that collided.
func dbSyncEnableIntake(tokenStdin, passphraseStdin, joinStdin bool, r2 dbSyncR2Flags) error {
	var asked []string
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

// dbSyncR2SecretStdin is the reader --r2-secret-stdin reads; a var so a test can feed it.
var dbSyncR2SecretStdin io.Reader = os.Stdin

// dbSyncEnableR2Tail fills an enable's frame and returns the raw tail: the
// token, then the passphrase, then the bucket secret. The order is the split the
// owner reads back from the end, and it is fixed here so the two halves cannot
// disagree about where a secret starts. The bucket secret comes from
// --r2-secret-stdin, else RELEVO_R2_SECRET; with neither the tail ends after the
// passphrase. The passphrase is placed rather than read here: its route is
// dbSyncPassphrase's, shared with unlock.
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
// handle open. Status stays a read because it is a description of the machine,
// and what it prints is a function of the document alone.
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

	doc, err := dbSyncStatusRead(local, shared.Origin())
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

// dbSyncStatusRead builds the status document from the machine-local rows, which
// is what lets status answer with the network blackholed. Origin names this
// machine within the account snapshot: its own row is replaced by the live
// counters rather than counted twice.
func dbSyncStatusRead(local relevosync.Local, origin string) (dbSyncStatusDoc, error) {
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
	}
	if err := dbSyncStatusStorage(local, settings, &doc); err != nil {
		return dbSyncStatusDoc{}, err
	}
	if err := dbSyncStatusMeter(local, origin, &doc); err != nil {
		return dbSyncStatusDoc{}, err
	}
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

// dbSyncStatusMeter populates the account reading and the idle-export hour from
// rows the sync loop already keeps, so neither reaches the network.
func dbSyncStatusMeter(local relevosync.Local, origin string, doc *dbSyncStatusDoc) error {
	now := time.Now()
	q, qok, err := relevosync.ReadQuota(local, origin, now)
	if err != nil {
		return dbSyncClassify(err)
	}
	if qok {
		doc.Account = &dbSyncAccountDoc{
			Month:     q.Account.Month,
			Total:     q.Account.Total,
			Machines:  q.Account.Machines,
			Own:       q.Own,
			Quota:     q.Quota,
			Projected: q.Projected,
			ReadAt:    statusStamp(q.Account.ReadAt),
			OverWarn:  q.OverWarn(),
			PaceOver:  q.PaceOver(),
		}
	}
	idle, iok, err := relevosync.IdleExports(local, now)
	if err != nil {
		return dbSyncClassify(err)
	}
	if iok {
		doc.IdleExports = &dbSyncIdleDoc{Hour: idle.Hour, Exported: idle.Exported, Idle: idle.Idle, Over: idle.OverIdle()}
	}
	return nil
}

// dbSyncR2Line is where bodies would go, or empty on a machine with no bucket.
// An unconfigured machine gets no clause at all: bodies in a bucket are opt-in,
// so absence is the ordinary state.
func dbSyncR2Line(doc dbSyncStatusDoc) string {
	if !doc.R2Configured {
		return ""
	}
	return doc.R2Endpoint + "/" + doc.R2Bucket
}

// dbSyncQuotasLine renders the month's traffic and the stored bytes against the
// document's quotas. A storage reading never taken reads as not measured, never as zero.
func dbSyncQuotasLine(doc dbSyncStatusDoc) string {
	var month relevosync.ByteCounters
	if doc.Bytes != nil {
		month = *doc.Bytes
	}
	var storage relevosync.Storage
	if doc.Storage != nil {
		storage = *doc.Storage
	}
	return relevosync.FormatQuotas(month, dbSyncAccount(doc), relevosync.Settings{
		QuotaTursoSync:    doc.QuotaTursoSync,
		QuotaTursoStorage: doc.QuotaTursoStorage,
		QuotaR2Storage:    doc.QuotaR2Storage,
	}, storage)
}
