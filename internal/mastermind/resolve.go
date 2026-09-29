package mastermind

import (
	"errors"
	"fmt"
	"time"
)

// ErrUnregisteredSession reports a detected session that has no mastermind
// record in the registry.
type ErrUnregisteredSession struct {
	Kind      string
	SessionID string
}

func (e ErrUnregisteredSession) Error() string {
	return fmt.Sprintf("no relevo mastermind for %s session %s", e.Kind, e.SessionID)
}

func (e ErrUnregisteredSession) Is(target error) bool {
	return target == ErrNoMasterMind
}

// Resolution says which step of Resolve's order produced a record.
type Resolution string

const (
	ResolutionFlag    Resolution = "flag"
	ResolutionEnv     Resolution = "env"
	ResolutionHost    Resolution = "host"
	ResolutionSession Resolution = "session"
)

// ResolveInput is everything Resolve may look at. Nothing is read from the
// process itself.
type ResolveInput struct {
	// Flag is --mastermind's value, empty when it was not given.
	Flag string
	// Env is os.Getenv in production. Nil reads as "no environment".
	Env func(string) string
	// PPID is os.Getppid(), Detect's fallback host pid.
	PPID int
	// ProcStart reads a process's start time in Unix seconds. A nil
	// ProcStart, or an error from it, is a fall-through, not an error.
	ProcStart func(pid int) (int64, error)
	// Now stamps the seen_at of whatever Resolve hits.
	Now time.Time
	// CWD is the caller's working directory; "" skips the opencode step.
	CWD string
	// OpencodeSession finds an opencode session id for CWD; nil skips the
	// opencode step.
	OpencodeSession func(cwd string, now time.Time) (string, error)
}

// Resolve is how every verb except `init` gets its mastermind: flag > env > host
// > session, first hit wins. It never creates a record; a missing hook is
// loud (ErrNoMasterMind) rather than a silently unnamed mastermind.
func Resolve(reg Registry, in ResolveInput) (Record, Resolution, error) {
	env := in.Env
	if env == nil {
		env = func(string) string { return "" }
	}

	if in.Flag != "" {
		rec, err := lookupRef(reg, in.Flag)
		if err != nil {
			return Record{}, "", err
		}
		return hit(reg, rec, ResolutionFlag, in.Now)
	}

	if rec, res, ok, err := resolveFromEnv(reg, env, in.Now); err != nil {
		return Record{}, "", err
	} else if ok {
		return rec, res, nil
	}

	ident, detected := Detect(env, in.PPID)

	// A ProcStart error falls through to the session step.
	if detected && ident.HostPID > 0 && in.ProcStart != nil {
		if startedAt, err := in.ProcStart(ident.HostPID); err == nil {
			rec, err := reg.ByHost(ident.HostPID, startedAt)
			switch {
			case err == nil:
				return hit(reg, rec, ResolutionHost, in.Now)
			case errors.Is(err, ErrNotFound):
			default:
				return Record{}, "", err
			}
		}
	}

	if detected && ident.Kind == "opencode" && ident.SessionID == "" {
		if in.OpencodeSession == nil || in.CWD == "" {
			detected = false
		} else {
			id, err := in.OpencodeSession(in.CWD, in.Now)
			switch {
			case err == nil:
				ident.SessionID = id
			case errors.Is(err, ErrNoOpencodeSession):
				detected = false
			default:
				return Record{}, "", err
			}
		}
	}

	if detected {
		rec, err := reg.BySession(ident.Kind, ident.SessionID)
		switch {
		case err == nil:
			return hit(reg, rec, ResolutionSession, in.Now)
		case errors.Is(err, ErrNotFound):
			if ident.Kind == "opencode" {
				return Record{}, "", ErrUnregisteredSession{Kind: "opencode", SessionID: ident.SessionID}
			}
		default:
			return Record{}, "", err
		}
	}

	return Record{}, "", ErrNoMasterMind
}

// resolveFromEnv is Resolve's env step: $RELEVO_MASTERMIND first, then the
// historical $RELEVO_PLANNER, because the export line already in a live
// session's $CLAUDE_ENV_FILE is state already written (D5). ok is false when
// neither variable names anything, in which case the caller keeps looking.
func resolveFromEnv(reg Registry, env func(string) string, now time.Time) (Record, Resolution, bool, error) {
	for _, name := range []string{"RELEVO_MASTERMIND", "RELEVO_PLANNER"} {
		ref := env(name)
		if ref == "" {
			continue
		}
		rec, err := lookupRef(reg, ref)
		if err != nil {
			return Record{}, "", false, err
		}
		rec, res, err := hit(reg, rec, ResolutionEnv, now)
		if err != nil {
			return Record{}, "", false, err
		}
		return rec, res, true, nil
	}
	return Record{}, "", false, nil
}

// lookupRef resolves ref as an id when it has that shape, else as a name.
func lookupRef(reg Registry, ref string) (Record, error) {
	lookup := reg.ByName
	if ValidID(ref) == nil {
		lookup = reg.Get
	}

	rec, err := lookup(ref)
	if err == nil {
		return rec, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Record{}, err
	}
	return Record{}, ErrUnknownMasterMind{Ref: ref}
}

// hit refreshes seen_at and reports how the record was found; Touch's error
// is dropped since a resolution must not fail on a timestamp write.
func hit(reg Registry, rec Record, res Resolution, now time.Time) (Record, Resolution, error) {
	_ = reg.Touch(rec.ID, now)
	return rec, res, nil
}
