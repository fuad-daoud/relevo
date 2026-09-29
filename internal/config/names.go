package config

import (
	"encoding/json"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/candidate"
)

// EnsureCandidateNames derives and stores names for candidates written before
// names existed; it is idempotent and only matters for legacy data.
func (s *Store) EnsureCandidateNames() (bool, error) {
	body, ok, err := s.Body(Candidates)
	if err != nil || !ok {
		return false, err
	}

	filled, changed, err := fillCandidateNames(body)
	if err != nil {
		return false, err
	}
	if !changed {
		return false, nil
	}

	if _, err := s.As("migration", "candidate names derived").Put(Candidates, filled); err != nil {
		return false, err
	}
	return true, nil
}

// fillCandidateNames edits decoded objects, so every extra key and the
// element order survive.
func fillCandidateNames(body []byte) (filled []byte, changed bool, err error) {
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(body, &rows); err != nil {
		return body, false, nil
	}

	missing := false
	for _, row := range rows {
		name, err := rowName(row)
		if err != nil {
			return body, false, nil
		}
		if name == "" {
			missing = true
			break
		}
	}
	if !missing {
		return body, false, nil
	}

	var entries []candidate.Candidate
	if err := json.Unmarshal(body, &entries); err != nil {
		return body, false, nil
	}
	names := candidate.DeriveNames(entries)
	if len(names) != len(rows) {
		return body, false, nil
	}

	for i, row := range rows {
		name, err := rowName(row)
		if err != nil {
			return body, false, nil
		}
		if name != "" {
			continue
		}
		raw, err := json.Marshal(names[i])
		if err != nil {
			return nil, false, err
		}
		row["name"] = raw
	}

	encoded, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", FileName(Candidates), err)
	}
	encoded = append(encoded, '\n')
	return encoded, true, nil
}

func rowName(row map[string]json.RawMessage) (string, error) {
	raw, ok := row["name"]
	if !ok {
		return "", nil
	}
	var name string
	if err := json.Unmarshal(raw, &name); err != nil {
		return "", err
	}
	return name, nil
}
