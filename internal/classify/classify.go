// Package classify splits an agent's report into paragraphs and asks a model
// whether each is an instruction to the reader rather than a status report.
package classify

import (
	"context"
	"errors"
	"fmt"
)

type Kind string

const (
	KindProse  Kind = "prose"
	KindFenced Kind = "fenced"
)

type Paragraph struct {
	Index int
	Kind  Kind
	Text  string
	Line  int
	Lines int
}

type Request struct {
	Source     string // "report" or "dialog"
	Harness    string
	Paragraphs []Paragraph // already Trim'd; len >= 1
}

type Answers struct {
	Model         string
	Probabilities []float64 // parallel to Request.Paragraphs; each in [0,1]
	InputTokens   int
}

type Status struct {
	Configured bool
	Provider   string
	Model      string
	KeySource  string // "env", "db", or ""
}

type Classifier interface {
	// Judge returns one probability per paragraph. Errors are transport or
	// configuration, never verdicts; ctx bounds the total including any retry.
	Judge(ctx context.Context, req Request) (Answers, error)
}

var ErrUnavailable = errors.New("classify: unavailable")   // configured, no key; Unavailable.Judge returns it wrapped with the reason
var ErrUnauthorized = errors.New("classify: unauthorized") // HTTP 401; never retried
var ErrBadRequest = errors.New("classify: bad request")    // HTTP 422; never retried; wraps the body's first 200 bytes
var ErrEmpty = errors.New("classify: no paragraphs")       // Judge called with zero paragraphs

type StatusError struct {
	Code int
	Body string /* first 200 bytes */
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("classify: http %d: %s", e.Code, e.Body)
}

type Unavailable struct {
	Reason string
}

func (u Unavailable) Judge(context.Context, Request) (Answers, error) {
	return Answers{}, fmt.Errorf("%w: %s", ErrUnavailable, u.Reason)
}
