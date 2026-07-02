// File: legacy/manager.go
// Purpose: Implements or tests the legacy TCP validation server.

package legacy

import (
	"hash/fnv"
	"net"
	"sync"
)

const connShardCount = 256

type connShard struct {
	mu    sync.RWMutex
	conns map[uint64]*Conn
}

// ConnManager stores active legacy TCP connections in shards.
type ConnManager struct {
	shards [connShardCount]*connShard
	ipMu   sync.Mutex
	ipConn map[string]int
}

// NewConnManager creates an empty connection manager.
func NewConnManager() *ConnManager {
	m := &ConnManager{ipConn: make(map[string]int)}
	for i := range m.shards {
		m.shards[i] = &connShard{conns: make(map[uint64]*Conn)}
	}
	return m
}

// Add records a connection and updates its per-IP count.
func (m *ConnManager) Add(c *Conn) {
	s := m.shard(c.id)
	s.mu.Lock()
	s.conns[c.id] = c
	s.mu.Unlock()

	m.ipMu.Lock()
	m.ipConn[c.remoteIP]++
	m.ipMu.Unlock()
}

// Remove deletes a connection and decrements its per-IP count.
func (m *ConnManager) Remove(c *Conn) {
	s := m.shard(c.id)
	s.mu.Lock()
	delete(s.conns, c.id)
	s.mu.Unlock()

	m.ipMu.Lock()
	if n := m.ipConn[c.remoteIP]; n <= 1 {
		delete(m.ipConn, c.remoteIP)
	} else {
		m.ipConn[c.remoteIP] = n - 1
	}
	m.ipMu.Unlock()
}

// CountByIP returns the active connection count for an IP.
func (m *ConnManager) CountByIP(ip string) int {
	m.ipMu.Lock()
	defer m.ipMu.Unlock()
	return m.ipConn[ip]
}

// Snapshot returns a copy of active connection pointers.
func (m *ConnManager) Snapshot() []*Conn {
	out := make([]*Conn, 0)
	for _, s := range m.shards {
		s.mu.RLock()
		for _, c := range s.conns {
			out = append(out, c)
		}
		s.mu.RUnlock()
	}
	return out
}

// CloseAll closes every active connection.
func (m *ConnManager) CloseAll() {
	for _, c := range m.Snapshot() {
		c.Close()
	}
}

func (m *ConnManager) shard(id uint64) *connShard {
	return m.shards[id%connShardCount]
}

func remoteIP(addr net.Addr) string {
	if addr == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	return host
}

func hashString(s string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return h.Sum32()
}
