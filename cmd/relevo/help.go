package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
)

type helpFlagValues struct {
	asJSON *bool
}

// helpFlagSet exists so the registry's parity test finds exactly one installer
// per verb.
func helpFlagSet(fs *flag.FlagSet) *helpFlagValues {
	v := &helpFlagValues{}
	v.asJSON = fs.Bool("json", false, "print the verb registry instead of the usage text")
	return v
}

// cmdHelp prints the human usage text, or with --json the verb registry. The
// usage text is the same constant every other human rendering points at.
func cmdHelp(args []string) error {
	fs := flag.NewFlagSet("help", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := helpFlagSet(fs)
	// -h on help itself still prints the text a human asked for.
	fs.Usage = func() { fmt.Fprint(fs.Output(), usage) }
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if !*v.asJSON {
		fmt.Print(usage)
		return nil
	}

	// A verb name can hold spaces ("config server add"), so the positionals
	// are joined rather than refused.
	rest := fs.Args()
	if len(rest) == 0 {
		return json.NewEncoder(os.Stdout).Encode(registryDocument())
	}
	name := strings.Join(rest, " ")
	entry, ok := registryEntry(name)
	if !ok {
		return fail(codeUsage, "unknown verb %q; run \"relevo help --json\" for the list", name)
	}
	return json.NewEncoder(os.Stdout).Encode(entry)
}
