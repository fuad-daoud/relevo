package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/fuad-daoud/relevo/internal/db/wire"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// TakeoverDoc is `relevo takeover --json`.
type TakeoverDoc struct {
	Name      string `json:"name"`
	OldWriter string `json:"old_writer"`
	Upto      int    `json:"upto"`
}

type takeoverFlagValues struct {
	name   *string
	force  *bool
	asJSON *bool
}

func takeoverFlagSet(fs *flag.FlagSet) *takeoverFlagValues {
	v := &takeoverFlagValues{}
	v.name = fs.String("name", "", "binding to take over")
	v.force = fs.Bool("force", false, "fence the old writer's later changes; required")
	v.asJSON = fs.Bool("json", false, "print the result as a JSON document")
	return v
}

func cmdTakeover(args []string) error {
	fs := flag.NewFlagSet("takeover", flag.ContinueOnError)
	v := takeoverFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	target, ok := explicitBinding(*v.name, fs.Args())
	if !ok {
		return fail(codeUsage, "usage: relevo takeover <name> --force%s\n"+
			"takeover makes this machine the writer of a binding another machine writes", bindingHint("takeover"))
	}
	rt, err := newRuntime()
	if err != nil {
		return writeError(err)
	}
	dec, err := relevo.Takeover(context.Background(), rt, relevo.TakeoverOptions{Name: target, Force: *v.force, Drain: drainImport})
	if err != nil {
		return writeError(err)
	}
	if *v.asJSON {
		return printDoc(TakeoverDoc{Name: target, OldWriter: dec.OldWriter, Upto: dec.Upto})
	}
	if dec.Warning != "" {
		fmt.Fprintln(os.Stderr, dec.Warning)
	}
	fmt.Print(relevo.TakeoverText(target, dec))
	return nil
}

// drainImport asks the daemon for one pull, so the fence is read after the old
// writer's log was imported as far as it goes.
func drainImport(ctx context.Context) error {
	shared, err := dialSyncVerb()
	if err != nil {
		return err
	}
	defer func() { _ = shared.Close() }()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	_, err = sendSyncVerb(ctx, shared, wire.SyncVerbPull, dbSyncVerbOptions{Timeout: time.Minute})
	return err
}
