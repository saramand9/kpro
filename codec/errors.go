// File: codec/errors.go
// Purpose: Defines or tests KPro wire codec encoding and decoding.

package codec

import "errors"

var (
	ErrFrameTooLarge = errors.New("frame too large")
	ErrBadMagic      = errors.New("bad frame magic")
	ErrBadVersion    = errors.New("bad frame version")
)
