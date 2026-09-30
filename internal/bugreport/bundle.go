// Package bugreport assembles a local, redacted diagnostic bundle: the
// sections a report needs, the privacy pass over them, the markdown and JSON
// renderings, and the failing-command slot a bundle reads. It sends nothing.
package bugreport

import (
	"fmt"
	"time"
)

// The bundle's section names: one identity per part, used as the heading, the
// word an omission line starts with, and the name a projection sets.
const (
	SectionEnvironment = "environment"
	SectionLastError   = "last_error"
	SectionDoctor      = "doctor"
	SectionStatus      = "status"
	SectionRounds      = "rounds"
	SectionHooks       = "hooks"
	SectionGates       = "gates"
	SectionDaemon      = "daemon"
	SectionJournal     = "journal"
	SectionLogs        = "logs"
)

// Section is one named part of the bundle: prose in Lines, a table when
// Columns is set, or Omitted saying why the part is missing rather than empty.
// A projection fills it from an allow-list, never from a whole document.
type Section struct {
	Name    string     `json:"name"`
	Columns []string   `json:"columns,omitempty"`
	Rows    [][]string `json:"rows,omitempty"`
	Lines   []string   `json:"lines,omitempty"`
	Omitted string     `json:"omitted,omitempty"`
}

// Omission records one source that errored or panicked: the section it would
// have filled and the reason it did not.
type Omission struct {
	Section string `json:"section"`
	Reason  string `json:"reason"`
}

// Source contributes one section to the bundle. Name is the section's identity
// when the section itself carries none.
type Source struct {
	Name  string
	Build func() (Section, error)
}

// Bundle is one assembled report: the header facts, the sections in order, and
// the sources that could not fill theirs.
type Bundle struct {
	Title     string
	Version   string
	Created   time.Time
	Raw       bool
	Privacy   []string
	Sections  []Section
	Omissions []Omission
}

// Collect runs every source once, in order, and assembles the bundle. A source
// that returns an error or panics becomes one omission and leaves every other
// section intact: assembling a bundle never fails the run that asked for it.
func Collect(title, version string, now time.Time, sources []Source) Bundle {
	b := Bundle{Title: title, Version: version, Created: now.UTC()}
	for _, src := range sources {
		sec, err := runSource(src)
		if err != nil {
			b.Omissions = append(b.Omissions, Omission{Section: src.Name, Reason: err.Error()})
			continue
		}
		if sec.Name == "" {
			sec.Name = src.Name
		}
		b.Sections = append(b.Sections, sec)
	}
	return b
}

// runSource runs one source, turning a panic into an error, so one bad source
// cannot take the bundle down with it.
func runSource(src Source) (sec Section, err error) {
	if src.Build == nil {
		return Section{}, fmt.Errorf("no builder")
	}
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic: %v", p)
		}
	}()
	return src.Build()
}
