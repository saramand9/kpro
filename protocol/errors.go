// File: protocol/errors.go
// Purpose: Defines or tests KPro wire protocol encoding and decoding.

package protocol

import "errors"

var (
	ErrFrameTooLarge = errors.New("frame too large")
	ErrBadMagic      = errors.New("bad frame magic")
	ErrBadVersion    = errors.New("bad frame version")
)
