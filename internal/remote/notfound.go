package remote

import (
	"errors"
)

// Every read in the catch-up takes this as "nothing to fetch".
func Is404(err error) bool {
	var statusErr httpStatusError
	return errors.As(err, &statusErr) && statusErr.HTTPStatus() == 404
}

// Matched structurally so this package never imports the client that reports it.
type httpStatusError interface {
	HTTPStatus() int
}
