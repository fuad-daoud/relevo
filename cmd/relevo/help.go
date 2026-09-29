package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
)

// helpFlagValues holds the pointer help parses into.
type helpFlagValues struct {
	asJSON *bool
}

// helpFlagSet defines that flag on fs and returns what it parses into.
func helpFlagSet(fs *flag.FlagSet) *helpFlagValues {
	v := &helpFlagValues{}
	v.asJSON = fs.Bool("json", false, "print the verb registry instead of the usage text")
	return v
}

// cmdHelp prints the human usage text, or with --json the verb registry: the
// whole document, or one verb when a name follows. The usage text is the same
// constant every other human rendering points at, byte for byte.
func cmdHelp(args []string) error {
	fs := flag.NewFlagSet("help", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := helpFlagSet(fs)
	// -h on help itself still prints the text a human asked for rather than
	// the flag package's own list.
	fs.Usage = func() { fmt.Fprint(fs.Output(), usage) }
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if !*v.asJSON {
		fmt.Print(usage)
		return nil
	}

	// A verb name can hold spaces ("config server add"), so the positionals
	// are joined rather than refused when there is more than one.
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
