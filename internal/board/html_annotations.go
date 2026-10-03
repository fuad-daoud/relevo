package board

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
)

// maxAnnotationBody is the largest annotations POST body accepted. The body
// carries a selector and a note and nothing else, so a ceiling far below the
// board's own cap cannot refuse a real comment.
const maxAnnotationBody = 64 << 10

// annotationPostDoc is what a successful post answers: the note that was
// written and the etag the file now carries, so the shell can post again
// without a re-read.
type annotationPostDoc struct {
	Annotation     Annotation `json:"annotation"`
	AnnotationsEtag string    `json:"annotationsEtag"`
}

// handleAnnotations is POST /api/annotations: the one route that writes. It is
// token-guarded like the read, so a page that has not been handed the token
// cannot author a note, and the token is never sent into the board frame.
//
// The author is set here and read from nowhere in the body: a board's own
// script shares the frame with the comment overlay and can post a pick, but it
// cannot post a note, and if it could it still could not sign it as anyone.
func (s *HTMLServer) handleAnnotations(w http.ResponseWriter, r *http.Request) {
	if !authorizedToken(r, s.Token) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	req, err := readAnnotationRequest(w, r)
	if err != nil {
		return
	}

	s.annotations.lock()
	defer s.annotations.unlock()

	ifMatch := r.Header.Get("If-Match")
	entry, etag, err := AppendAnnotation(s.BoardPath, req, &ifMatch)
	if err != nil {
		writeAnnotationError(w, err)
		return
	}
	writeJSONStatus(w, http.StatusCreated, annotationPostDoc{Annotation: entry, AnnotationsEtag: etag})
}

// readAnnotationRequest decodes and bounds the body. An oversized body is 413
// rather than a truncated read: a note that lost its tail is worse refused
// than one that never started. A body that does not decode is 422 here, since
// only the input is wrong.
func readAnnotationRequest(w http.ResponseWriter, r *http.Request) (AnnotationRequest, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxAnnotationBody+1))
	if err != nil {
		http.Error(w, "could not read the body", http.StatusBadRequest)
		return AnnotationRequest{}, err
	}
	if len(body) > maxAnnotationBody {
		http.Error(w, fmt.Sprintf("body is over the %d byte cap", maxAnnotationBody),
			http.StatusRequestEntityTooLarge)
		return AnnotationRequest{}, errors.New("annotation body is oversized")
	}
	var in struct {
		Selector string  `json:"selector"`
		X        float64 `json:"x"`
		Y        float64 `json:"y"`
		Text     string  `json:"text"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		http.Error(w, "the body is not a JSON annotation", http.StatusUnprocessableEntity)
		return AnnotationRequest{}, err
	}
	return AnnotationRequest{
		Selector: in.Selector,
		X:        in.X,
		Y:        in.Y,
		Text:     in.Text,
		By:       humanBy,
	}, nil
}

// writeAnnotationError maps a store refusal onto its status: a stale if-match is
// 409 so the shell can show its dirty banner, a board that is not there is 404,
// and everything the caller got wrong -- an empty note, a bad fraction, a
// malformed file -- is 422.
func writeAnnotationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrConflict):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, ErrNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	default:
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
	}
}

// boardAnnotations reads the notes beside the board for the board document. A
// missing file is an empty list and an empty etag. A file that is malformed or
// symlinked is not allowed to break the board: the notes are reported as
// unreadable and the board still renders, because a bad comment must never
// cost someone their board.
func boardAnnotations(boardPath string) (entries []Annotation, etag, problem string) {
	entries, etag, err := ReadAnnotations(boardPath)
	if err != nil {
		return []Annotation{}, "", err.Error()
	}
	return entries, etag, ""
}

// writeJSONStatus is writeJSON with a status, for the one create answer.
func writeJSONStatus(w http.ResponseWriter, status int, doc any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(doc)
}

// annotationsUnchanged answers the conditional GET: when the caller already has
// both the board's etag and the annotations' etag, the answer is 204 and no
// body. Either one moved and the shell gets the whole document again, which is
// why both are compared and neither is compared alone.
func annotationsUnchanged(r *http.Request, boardEtag, annEtag string) bool {
	if !r.URL.Query().Has("etag") || !r.URL.Query().Has("aetag") {
		return false
	}
	return r.URL.Query().Get("etag") == boardEtag && r.URL.Query().Get("aetag") == annEtag
}

// annotationLock is the in-process half of the concurrency rule. It covers the
// read-compare-write window so two overlapping posts in this server cannot
// both pass the same if-match. It is deliberately not a lock file: Windows is
// a build target and syscall.Flock does not exist there. What remains is
// documented rather than solved -- a CLI append and a page post landing in the
// same instant can still lose one of the two, because there is no cross-process
// lock.
type annotationLock struct{ mu sync.Mutex }

// lock takes the write window, or waits for it.
func (l *annotationLock) lock() { l.mu.Lock() }

// unlock releases the write window.
func (l *annotationLock) unlock() { l.mu.Unlock() }
