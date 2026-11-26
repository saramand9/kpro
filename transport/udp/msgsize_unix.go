//go:build unix || js || wasip1

package udp

import "syscall"

const errMessageTooLong = syscall.EMSGSIZE
