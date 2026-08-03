package core

import "errors"

// ErrOutboundBufferUnsupported reports that a session exists but its selected
// codec or transport cannot provide the owned-buffer fast path.
var ErrOutboundBufferUnsupported = errors.New("outbound buffer fast path unsupported")
