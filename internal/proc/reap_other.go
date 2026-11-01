//go:build !unix

package proc

// InheritedReaper has no implementation off unix, where there is no wait4 and no
// re-exec; it keeps the same API so callers need no build tag of their own.
type InheritedReaper struct{}

func NewInheritedReaper(self int, procRoot string) *InheritedReaper {
	return &InheritedReaper{}
}

func (r *InheritedReaper) Reap() []int { return nil }
