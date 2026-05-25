package kpro

import (
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

type Conn struct {
	id       uint64
	server   *Server
	raw      net.Conn
	remoteIP string

	sendQueue chan []byte
	done      chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup

	closed   int32
	lastSeen int64
	inflight int32
}

func (c *Conn) ID() uint64 {
	return c.id
}

func (c *Conn) RemoteIP() string {
	return c.remoteIP
}

func (c *Conn) LastSeen() time.Time {
	return time.Unix(0, atomic.LoadInt64(&c.lastSeen))
}

func (c *Conn) Send(msg Message) error {
	if atomic.LoadInt32(&c.closed) != 0 {
		return ErrClosed
	}
	frame, err := c.server.config.Protocol.Encode(msg)
	if err != nil {
		return err
	}
	select {
	case c.sendQueue <- frame:
		return nil
	case <-c.done:
		return ErrClosed
	default:
		atomic.AddInt64(&c.server.metrics.sendQueueFull, 1)
		c.Close()
		return ErrSendQueueFull
	}
}

func (c *Conn) Close() {
	c.closeOnce.Do(func() {
		atomic.StoreInt32(&c.closed, 1)
		close(c.done)
		_ = c.raw.Close()
		c.server.conns.Remove(c)
		c.server.metrics.decActive()
	})
}

func (c *Conn) start() {
	c.touch()
	c.wg.Add(2)
	go c.readLoop()
	go c.writeLoop()
}

func (c *Conn) touch() {
	atomic.StoreInt64(&c.lastSeen, time.Now().UnixNano())
}

func (c *Conn) readLoop() {
	defer c.wg.Done()
	defer c.Close()

	for {
		if err := c.raw.SetReadDeadline(time.Now().Add(c.server.config.ReadTimeout)); err != nil {
			atomic.AddInt64(&c.server.metrics.readErrors, 1)
			return
		}
		result, err := c.server.config.Protocol.Read(c.raw, c.server.config.MaxFrameSize)
		if err != nil {
			if err != io.EOF {
				atomic.AddInt64(&c.server.metrics.readErrors, 1)
			}
			if err == ErrBadMagic || err == ErrBadVersion || err == ErrFrameTooLarge {
				atomic.AddInt64(&c.server.metrics.decodeErrors, 1)
			}
			return
		}
		msg := result.Message
		c.touch()
		atomic.AddInt64(&c.server.metrics.framesIn, 1)
		atomic.AddInt64(&c.server.metrics.bytesIn, int64(result.Bytes))

		if msg.ID == MsgHeartbeat {
			_ = c.Send(Message{ID: MsgPong, Seq: msg.Seq})
			continue
		}
		if atomic.AddInt32(&c.inflight, 1) > c.server.config.MaxInflightPerConn {
			atomic.AddInt32(&c.inflight, -1)
			atomic.AddInt64(&c.server.metrics.inflightRejected, 1)
			c.Close()
			return
		}
		task := requestTask{conn: c, msg: msg}
		select {
		case c.server.tasks <- task:
		case <-c.done:
			atomic.AddInt32(&c.inflight, -1)
			return
		default:
			atomic.AddInt32(&c.inflight, -1)
			atomic.AddInt64(&c.server.metrics.workerQueueFull, 1)
			c.Close()
			return
		}
	}
}

func (c *Conn) writeLoop() {
	defer c.wg.Done()

	for {
		select {
		case data := <-c.sendQueue:
			if err := c.raw.SetWriteDeadline(time.Now().Add(c.server.config.WriteTimeout)); err != nil {
				atomic.AddInt64(&c.server.metrics.writeErrors, 1)
				c.Close()
				return
			}
			if err := writeFull(c.raw, data); err != nil {
				atomic.AddInt64(&c.server.metrics.writeErrors, 1)
				c.Close()
				return
			}
			atomic.AddInt64(&c.server.metrics.framesOut, 1)
			atomic.AddInt64(&c.server.metrics.bytesOut, int64(len(data)))
		case <-c.done:
			return
		}
	}
}
