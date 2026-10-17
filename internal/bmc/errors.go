package bmc

import "fmt"

// FormatError prefixes err with the operation that failed, keeping it unwrappable.
func FormatError(op string, err error) error {
	return fmt.Errorf("[%s] %w", op, err)
}
