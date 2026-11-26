package udp

// Plan 9 does not expose a distinct datagram-size errno.
var errMessageTooLong error
