package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/fuad-daoud/relevo/internal/bugreport"
	"github.com/fuad-daoud/relevo/internal/store"
)

// bugreportFlagValues holds the pointers bugreport parses into.
type bugreportFlagValues struct {
	gh     *bool
	asJSON *bool
	logs   *bool
	name   *string
	out    *string
	raw    *bool
	round  *int
	stdout *bool
}

// bugreportFlagSet defines that flag on fs and returns what it parses into, so
// the registry's parity test finds exactly one installer per verb.
func bugreportFlagSet(fs *flag.FlagSet) *bugreportFlagValues {
	v := &bugreportFlagValues{}
	v.gh = fs.Bool("gh", false, "file the bundle with gh issue create after writing it")
	v.asJSON = fs.Bool("json", false, "print the bundle as one JSON document and write no file")
	v.logs = fs.Bool("logs", false, "add one round's report, diff and transcript tail per selected binding")
	v.name = fs.String("name", "", "the binding the round sections read")
	v.out = fs.String("out", "", "write the markdown to exactly this path instead of the dated default")
	v.raw = fs.Bool("raw", false, "skip the redaction pass and mark the bundle raw")
	v.round = fs.Int("round", 0, "the round to read (requires --name)")
	v.stdout = fs.Bool("stdout", false, "print the markdown and write no file")
	return v
}

// bugreportExec runs one external command for the verb -- the gh file command
// and the journal reader. It is a seam every test replaces, so no test spawns
// either binary.
var bugreportExec = func(ctx context.Context, argv []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	return cmd.Output()
}

// bugreportOptions is what one run of the verb was asked for.
type bugreportOptions struct {
	name   string
	round  int
	logs   bool
	raw    bool
	out    string
	stdout bool
	asJSON bool
	gh     bool
}

// validate refuses the combinations the verb's contract makes exclusive,
// before any runtime is built: the three output modes pick one sink, a path is
// not a mode, and a round is a round of one named binding.
func (o bugreportOptions) validate() error {
	modes := 0
	for _, on := range []bool{o.stdout, o.asJSON, o.gh} {
		if on {
			modes++
		}
	}
	if modes > 1 {
		return fail(codeUsage, "--stdout, --json and --gh are mutually exclusive")
	}
	if o.out != "" && (o.stdout || o.asJSON) {
		return fail(codeUsage, "--out writes a file; do not combine it with --stdout or --json")
	}
	if o.round != 0 && o.name == "" {
		return fail(codeUsage, "--round reads a round of one binding; pass --name")
	}
	return nil
}

func cmdBugreport(args []string) error {
	fs := flag.NewFlagSet("relevo bugreport", flag.ContinueOnError)
	v := bugreportFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	opts := bugreportOptions{
		name: *v.name, round: *v.round, logs: *v.logs, raw: *v.raw,
		out: *v.out, stdout: *v.stdout, asJSON: *v.asJSON, gh: *v.gh,
	}
	if err := opts.validate(); err != nil {
		return err
	}
	return runBugreport(opts)
}

// runBugreport assembles, redacts and emits one bundle. It fails only on its
// own output: a usage error was refused before this, an unwritable path is
// internal, and a missing gh is not_available. A failing source is a line in
// the bundle, never a failed run.
func runBugreport(opts bugreportOptions) error {
	root, err := store.DefaultRoot()
	if err != nil {
		return fail(codeInternal, "%v", err)
	}
	rt, L, err := newRuntimeReadOnly()
	if err != nil {
		return fail(codeInternal, "%v", err)
	}
	if rt.DB != nil {
		defer func() { _ = rt.DB.Close() }()
	}

	le, haveLE, err := bugreport.ReadLastError(root)
	if err != nil {
		le, haveLE = bugreport.LastError{}, false
	}

	version := buildVersion()
	b := bugreport.Collect(
		bugreport.Title(version, le, haveLE),
		version,
		time.Now(),
		bugreportSources(rt, root, L, le, haveLE, opts),
	)
	r := bugreport.NewRedactor()
	r.Raw = opts.raw
	b = bugreport.Redact(b, r)

	switch {
	case opts.asJSON:
		return printDoc(b.Doc())
	case opts.stdout:
		fmt.Print(bugreport.Markdown(b))
		return nil
	}

	path := opts.out
	if path == "" {
		path = filepath.Join(root, "bugreports", "bugreport-"+time.Now().UTC().Format("20060102T150405Z")+".md")
	}
	if err := writeBundleFile(path, bugreport.Markdown(b)); err != nil {
		return fail(codeInternal, "%v", err)
	}

	argv := bugreport.IssueArgv(b.Title, path)
	line := bugreport.ShellLine(argv)
	fmt.Println(path)
	fmt.Println(line)
	if !opts.gh {
		return nil
	}
	if _, err := bugreportExec(context.Background(), argv); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return fail(codeNotAvailable, "%s", line)
		}
		return fail(codeInternal, "%v", err)
	}
	return nil
}

// writeBundleFile writes the markdown to path, creating the parent directory
// when it is missing. The file is owner-only: a bundle carries paths, names
// and whatever the redaction pass could not know.
func writeBundleFile(path string, text string) error {
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
