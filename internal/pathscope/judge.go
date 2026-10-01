package pathscope

import "strings"

// Change is one path git reported between a round's baseline tree and its
// closed tree, from `git diff --raw --no-renames`. Status is git's status
// letter ("A", "M", "D", "T", ...); OldMode and NewMode are the octal modes,
// "000000" when that side is absent; OldOID and NewOID are blob ids, all
// zeroes when that side is absent.
type Change struct {
	Status  byte
	OldMode string
	NewMode string
	OldOID  string
	NewOID  string
	Path    string
}

// Violation names one changed path a scope refuses and the reason it does.
type Violation struct {
	Path   string
	Reason string
}

// The reasons a path is refused, one per case in the judging order.
const (
	ReasonSymlinkOrGitlink   = "symlink or gitlink"
	ReasonModeChange         = "file mode changed"
	ReasonCodeChange         = "code change"
	ReasonDirectiveComment   = "directive comment"
	ReasonCannotJudge        = "cannot judge"
	ReasonNewOrDeletedSource = "new/deleted source file"
	ReasonOutsideScope       = "outside scope; cannot judge"
)

// Judge walks changes in the order git reported them and returns one
// Violation per path the scope refuses. readBlob reads one blob's contents by
// its object id; it is not called for paths a later case already settled.
//
// It is pure: git access goes through readBlob.
func Judge(scope Scope, changes []Change, readBlob func(oid string) ([]byte, error)) []Violation {
	var out []Violation
	for _, c := range changes {
		if v, refused := judgeOne(scope, c, readBlob); refused {
			out = append(out, v)
		}
	}
	return out
}

// judgeOne applies the cases, in order, to one change.
func judgeOne(scope Scope, c Change, readBlob func(oid string) ([]byte, error)) (Violation, bool) {
	// 1. A symlink or gitlink on either side, or a mode change between two
	// present sides.
	if c.OldMode == "120000" || c.NewMode == "120000" ||
		c.OldMode == "160000" || c.NewMode == "160000" {
		return Violation{Path: c.Path, Reason: ReasonSymlinkOrGitlink}, true
	}
	if c.OldMode != "" && c.NewMode != "" &&
		c.OldMode != "000000" && c.NewMode != "000000" &&
		c.OldMode != c.NewMode {
		return Violation{Path: c.Path, Reason: ReasonModeChange}, true
	}

	// 2. A path the scope's patterns name is in scope, whatever the status.
	if scope.Match(c.Path) {
		return Violation{}, false
	}

	if !strings.HasSuffix(c.Path, ".go") {
		// 8. Anything else: another extension, a binary, config, testdata.
		return Violation{Path: c.Path, Reason: ReasonOutsideScope}, true
	}

	switch c.Status {
	case 'M':
		if !scope.Comments {
			// The scope refuses comment judgement, and the path is out of
			// scope; nothing else can judge it.
			return Violation{Path: c.Path, Reason: ReasonOutsideScope}, true
		}
		oldSrc, err := readBlob(c.OldOID)
		if err != nil {
			return Violation{Path: c.Path, Reason: ReasonCannotJudge}, true
		}
		newSrc, err := readBlob(c.NewOID)
		if err != nil {
			return Violation{Path: c.Path, Reason: ReasonCannotJudge}, true
		}
		if ok, reason := judgeGo(oldSrc, newSrc); !ok {
			return Violation{Path: c.Path, Reason: reason}, true
		}
		return Violation{}, false
	case 'A', 'D':
		// 7. A .go file added or deleted.
		return Violation{Path: c.Path, Reason: ReasonNewOrDeletedSource}, true
	default:
		// 8. A status the Go comment rule cannot judge (a type change, a
		// rename git did not flatten, ...).
		return Violation{Path: c.Path, Reason: ReasonOutsideScope}, true
	}
}
