package store

import (
	"errors"
	"fmt"
	"strings"

	"github.com/fuad-daoud/relevo/internal/db"
)

// ErrHandedOver reports a binding this machine gave to another machine: its
// record is that machine's to write now, so this one neither loads nor sends it.
var ErrHandedOver = errors.New("binding handed over")

// HandedOverError names where a binding went.
type HandedOverError struct {
	Name string
	// To is the installation id and Label the name it reports.
	To    string
	Label string
	Kind  string
}

func (e *HandedOverError) Error() string {
	who := stripControls(e.Label)
	if who == "" {
		who = stripControls(e.To)
	}
	name := stripControls(e.Name)
	if e.Kind == db.LeftTakeover {
		return fmt.Sprintf("binding %q was taken over by %s; relevo unbind it here or leave it", name, who)
	}
	return fmt.Sprintf("binding %q is handed over to %s; take it back there with relevo takeover %s --force", name, who, name)
}

func (e *HandedOverError) Is(target error) bool { return target == ErrHandedOver }

// handedOver is the error load returns for a name with no live record here but
// a left row: the binding exists, on another machine.
func (s *Store) handedOver(d *db.DB, name string) error {
	left, found, err := d.BindingLeftByName(s.owner, name)
	if err != nil || !found {
		return nil
	}
	label := left.ToWriter
	if list, lerr := d.InstallationList(); lerr == nil {
		for _, in := range list {
			if in.ID == left.ToWriter && in.Label != "" {
				label = in.Label
			}
		}
	}
	return &HandedOverError{Name: name, To: stripControls(left.ToWriter), Label: stripControls(label), Kind: left.Kind}
}

// stripControls drops the C0, DEL and C1 bytes a terminal would act on: the
// writer text comes from a peer's row and reaches stderr. internal/view owns the
// same rule but imports this package.
func stripControls(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return -1
		}
		return r
	}, s)
}

// HandedOver reports whether this machine gave name away, and to whom.
func (s *Store) HandedOver(name string) (db.BindingLeft, bool, error) {
	d, err := s.dbForRead()
	if err != nil || d == nil {
		return db.BindingLeft{}, false, err
	}
	return d.BindingLeftByName(s.owner, name)
}

// Left lists every binding this machine gave away.
func (s *Store) Left() ([]db.BindingLeft, error) {
	d, err := s.dbForRead()
	if err != nil || d == nil {
		return nil, err
	}
	return d.BindingsLeft()
}
