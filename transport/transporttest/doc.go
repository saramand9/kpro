// Package transporttest provides a reusable behavioral contract suite for
// KPro message transports.
//
// Transport implementations supply a fresh server and a protocol-native test
// peer for each subtest. The suite verifies the common one-shot lifecycle and
// message/session behavior, then verifies every optional capability exposed by
// the server or session. Requirements can make selected optional capabilities
// mandatory for an implementation.
package transporttest
