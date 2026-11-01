package remote

import (
	"errors"
)

// Is404 reports whether err is the server's answer that a round file does not
// exist, which every read in the catch-up takes as "nothing to fetch".
func Is404(err error) bool {
	var statusErr httpStatusError
	return errors.As(err, &statusErr) && statusErr.HTTPStatus() == 404
}

// httpStatusError is the status an HTTP failure carries, matched
// structurally so this package never imports the client that reports it.
type httpStatusError interface {
	HTTPStatus() int
}
