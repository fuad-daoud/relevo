package main

import (
	"errors"
	"time"

	"github.com/fuad-daoud/relevo/internal/bugreport"
	"github.com/fuad-daoud/relevo/internal/tmpdir"
)

// recordInternal leaves the error loop's record behind for the bundle command
// to read. It runs between run and report, so the failure it is about is still
// the one being rendered.
//
// It records a coded `internal` failure and nothing else; a run of `bugreport`
// never records, so a bundle cannot record itself. Its own failures never
// change the original error, its rendering or its exit code.
func recordInternal(args []string, err error) {
	defer func() { _ = recover() }()

	var ce *cliError
	if !errors.As(err, &ce) || ce.code != codeInternal {
		return
	}
	if len(args) > 0 && args[0] == "bugreport" {
		return
	}
	root, rootErr := tmpdir.DefaultRoot()
	if rootErr != nil {
		return
	}
	_ = bugreport.WriteLastError(root, bugreport.LastError{
		Time:    time.Now(),
		Version: buildVersion(),
		Verb:    verbOf(args),
		Argv:    append([]string(nil), args...),
		Code:    string(ce.code),
		Message: ce.message,
		Next:    ce.next,
	})
}

// verbOf is the verb a record names: the run's first argument, or "" when the
// run carried none.
func verbOf(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}
