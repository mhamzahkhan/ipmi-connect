package bmc

import (
	"errors"
	"fmt"
	"net/url"
)

// FormatError prefixes err with the operation that failed, keeping it unwrappable.
// URLs inside *url.Error lose their query string, which carries session and CSRF tokens.
func FormatError(op string, err error) error {
	return fmt.Errorf("[%s] %w", op, redactURLError(err))
}

func redactURLError(err error) error {
	var ue *url.Error
	if !errors.As(err, &ue) {
		return err
	}
	redacted := *ue
	if u, parseErr := url.Parse(ue.URL); parseErr == nil {
		u.RawQuery = ""
		redacted.URL = u.String()
	} else {
		redacted.URL = "[redacted]"
	}
	if ue == err {
		return &redacted
	}
	// err wraps the *url.Error; keep the outer chain readable without the token.
	return fmt.Errorf("%s: %w", redacted.Error(), ue.Err)
}
