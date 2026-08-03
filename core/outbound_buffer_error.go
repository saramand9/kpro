package core

import "errors"

// ErrInvalidOutboundBuffer reports invalid size or repeated use of an owned
// outbound buffer reservation.
var ErrInvalidOutboundBuffer = errors.New("invalid or already consumed outbound buffer")
