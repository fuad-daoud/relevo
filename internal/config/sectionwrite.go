package config

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// Put stores body as sec in one transaction; a refused body writes nothing. A
// write the next Load would refuse is refused here.
//
// Machine fields leave the body for local overrides before anything is
// stored, and validation reads the merged document: a body omitting an
// account's config dir is valid because the local row still supplies it.
func (s *Store) Put(sec Section, body []byte) ([]string, error) {
	if sec == Candidates {
		filled, _, err := fillCandidateNames(body)
		if err != nil {
			return nil, invalidValueOf(err)
		}
		body = filled
	}

	var warnings []string
	if err := s.db.Tx(func(t *db.Tx) error {
		before, err := readSnapshot(t)
		if err != nil {
			return err
		}
		body, err = s.normalizeSectionBody(sec, body)
		if err != nil {
			return invalidValueOf(err)
		}
		sharedBody, patches, err := splitMachineFields(sec, body)
		if err != nil {
			return invalidValueOf(err)
		}
		patches, err = s.clearsFor(t, sec, body, patches)
		if err != nil {
			return err
		}
		if err := writeLocalPatches(t, patches, s.now().UTC()); err != nil {
			return err
		}

		prospective := copyDoc(before.doc)
		prospective[sec] = sharedBody
		merged, err := mergedDoc(t, prospective)
		if err != nil {
			return invalidValueOf(err)
		}
		if err := validateProspective(merged); err != nil {
			return err
		}
		warnings, err = Validate(sec, merged[sec])
		if err != nil {
			return invalidValueOf(err)
		}
		if err := s.validateHooksBody(sharedBody); sec == Hooks && err != nil {
			return invalidValueOf(err)
		}
		return s.writeRecord(t, before, sec, sharedBody)
	}); err != nil {
		return nil, err
	}
	s.captureAfterHooksWrite(sec)
	return warnings, nil
}

// Delete removes sec's stored body, if any, in one transaction. The version
// still bumps when sec was absent: it is a change counter, not a section
// count. A removal the next Load would refuse is refused here.
//
// Machine fields leave with the shared body and stay local, so removing a
// section never takes this machine's directories with it.
func (s *Store) Delete(sec Section) error {
	if err := s.db.Tx(func(t *db.Tx) error {
		before, err := readSnapshot(t)
		if err != nil {
			return err
		}
		if _, patches, err := splitMachineFields(sec, before.doc[sec]); err != nil {
			return err
		} else if err := writeLocalPatches(t, patches, s.now().UTC()); err != nil {
			return err
		}
		merged, err := mergedDoc(t, docWithout(before.doc, sec))
		if err != nil {
			return err
		}
		if err := validateProspective(merged); err != nil {
			return err
		}
		return s.deleteRecord(t, before, sec)
	}); err != nil {
		return err
	}
	s.captureAfterHooksWrite(sec)
	return nil
}

// validateNamedSections runs the per-section checks needing a config root,
// which decodeDoc cannot do with a document alone. A section without a
// root-dependent rule is skipped.
func (s *Store) validateNamedSections(doc Doc) error {
	for _, sec := range Sections {
		body, ok := doc[sec]
		if !ok || sec != Hooks {
			continue
		}
		if err := s.validateHooksBody(body); err != nil {
			return err
		}
	}
	return nil
}

// PutDoc stores every section in doc in one transaction, validating every
// body first; an unknown section or the first invalid body aborts with
// nothing written. Sections absent from doc are untouched. A write the next
// Load would refuse is refused here. Machine fields of every named section
// split out as Put splits them.
func (s *Store) PutDoc(doc map[Section]json.RawMessage) ([]string, error) {
	if err := checkDocSections(doc); err != nil {
		return nil, invalidValueOf(err)
	}

	if body, ok := doc[Candidates]; ok {
		filled, _, err := fillCandidateNames(body)
		if err != nil {
			return nil, invalidValueOf(err)
		}
		doc[Candidates] = filled
	}

	// The hooks section is divided by path before anything is written, so an
	// argv row naming a file outside this machine's config root lands local
	// rather than shared, as Put divides it.
	if body, ok := doc[Hooks]; ok {
		normalized, err := s.normalizeSectionBody(Hooks, body)
		if err != nil {
			return nil, invalidValueOf(err)
		}
		doc[Hooks] = normalized
	}

	var warnings []string
	now := s.now().UTC()
	if err := s.db.Tx(func(t *db.Tx) error {
		before, err := readSnapshot(t)
		if err != nil {
			return err
		}
		prospective, err := s.stripMachineFields(t, before.doc, doc, now)
		if err != nil {
			return err
		}
		merged, err := mergedDoc(t, prospective)
		if err != nil {
			return err
		}
		if err := validateProspective(merged); err != nil {
			return err
		}
		warnings, err = docWarnings(doc, merged)
		if err != nil {
			return err
		}
		if err := s.validateNamedSections(merged); err != nil {
			return invalidValueOf(err)
		}
		for _, sec := range Sections {
			if _, named := doc[sec]; !named {
				continue
			}
			if err := s.writeSection(t, sec, prospective[sec]); err != nil {
				return err
			}
		}
		return s.record(t, before, nil)
	}); err != nil {
		return nil, err
	}
	if _, named := doc[Hooks]; named {
		s.captureAfterHooksWrite(Hooks)
	}
	return warnings, nil
}

// clearsFor adds the nulls dropping machine fields the write no longer
// carries, judged against the merged stored body rather than the shared one:
// the shared body already lacks those fields, so comparing against it would
// clear nothing.
func (s *Store) clearsFor(t *db.Tx, sec Section, body json.RawMessage, patches map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	stored, ok, err := t.ConfigGet(string(sec))
	if err != nil || !ok {
		return patches, err
	}
	merged, err := mergedBody(t, sec, stored)
	if err != nil {
		return patches, err
	}
	return clearedFields(sec, merged, body, patches), nil
}

// stripMachineFields writes the overrides each section of doc implies and
// returns the prospective shared document: before.doc with the machine fields
// of each named section taken out.
func (s *Store) stripMachineFields(t *db.Tx, before Doc, doc Doc, now time.Time) (Doc, error) {
	prospective := copyDoc(before)
	for _, sec := range Sections {
		body, ok := doc[sec]
		if !ok {
			continue
		}
		body, err := s.normalizeSectionBody(sec, body)
		if err != nil {
			return nil, invalidValueOf(err)
		}
		sharedBody, patches, err := splitMachineFields(sec, body)
		if err != nil {
			return nil, invalidValueOf(err)
		}
		if patches, err = s.clearsFor(t, sec, body, patches); err != nil {
			return nil, err
		}
		if err := writeLocalPatches(t, patches, now); err != nil {
			return nil, err
		}
		prospective[sec] = sharedBody
	}
	return prospective, nil
}

// docWarnings returns the warnings of every section doc names, read off the
// merged bodies rather than the shared halves: the warned document is the one
// a reader gets.
func docWarnings(doc, merged Doc) ([]string, error) {
	var warnings []string
	for _, sec := range Sections {
		if _, named := doc[sec]; !named {
			continue
		}
		w, err := Validate(sec, merged[sec])
		if err != nil {
			return nil, err
		}
		warnings = append(warnings, w...)
	}
	return warnings, nil
}

func checkDocSections(doc map[Section]json.RawMessage) error {
	var unknown []string
	for sec := range doc {
		if !known(sec) {
			unknown = append(unknown, string(sec))
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return fmt.Errorf("unknown config section %q", unknown[0])
}

func known(sec Section) bool {
	for _, s := range Sections {
		if s == sec {
			return true
		}
	}
	return false
}
