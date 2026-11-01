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

// Judge walks changes in git's order and returns one Violation per refused path.
// readBlob reads one blob by id; it is not called for settled paths. It is
// pure: git access goes through readBlob.
func Judge(scope Scope, changes []Change, readBlob func(oid string) ([]byte, error)) []Violation {
	var out []Violation
	for _, c := range changes {
		if v, refused := judgeOne(scope, c, readBlob); refused {
			out = append(out, v)
		}
	}
	return out
}

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

	// 3-5. Comment judgement, by extension: .go keeps the go/scanner rule,
	// a table extension gets the code-line rule, and any other extension, a
	// binary or an extensionless path is refused as before.
	l := langFor(c.Path)
	if l == nil && !strings.HasSuffix(c.Path, ".go") {
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
		if l != nil {
			if ok, reason := judgeComments(l, oldSrc, newSrc); !ok {
				return Violation{Path: c.Path, Reason: reason}, true
			}
			return Violation{}, false
		}
		if ok, reason := judgeGo(oldSrc, newSrc); !ok {
			return Violation{Path: c.Path, Reason: reason}, true
		}
		return Violation{}, false
	case 'A', 'D':
		// 7. A source file added or deleted, .go or a table language: a new
		// file has no old side to judge.
		return Violation{Path: c.Path, Reason: ReasonNewOrDeletedSource}, true
	default:
		// 8. A status the comment rules cannot judge (a type change, a
		// rename git did not flatten, ...).
		return Violation{Path: c.Path, Reason: ReasonOutsideScope}, true
	}
}
