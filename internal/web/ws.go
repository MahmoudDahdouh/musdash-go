package web

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// A WebSocket, as much of one as the terminal needs (RFC 6455): the
// handshake, whole messages in either direction, ping and close. There are
// no extensions and no compression, and a message from the browser is at
// most wsMaxMessage long. The terminal is its only user; a library would
// have been a module for the sake of two hundred lines.

const (
	wsContinuation = 0x0
	wsText         = 0x1
	wsBinary       = 0x2
	wsClose        = 0x8
	wsPing         = 0x9
	wsPong         = 0xA

	// wsMaxMessage is the longest message accepted from a browser. What is
	// typed or pasted into a terminal is far shorter; the bound is what one
	// connection can make this process hold.
	wsMaxMessage = 64 << 10

	// Close codes.
	wsNormal    = 1000
	wsProtocol  = 1002
	wsPolicy    = 1008
	wsTooBig    = 1009
	wsGoingAway = 1001

	wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
)

var errWSProtocol = errors.New("websocket: the browser broke the protocol")

// wsConn is one accepted WebSocket.
type wsConn struct {
	conn net.Conn
	r    *bufio.Reader

	// Writes come from more than one goroutine: output, pings, the close.
	wmu    sync.Mutex
	closed bool
}

// headerHas reports whether a comma-separated header names a token.
func headerHas(h http.Header, name, token string) bool {
	for _, value := range h.Values(name) {
		for _, part := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}

// acceptWS answers a WebSocket handshake and takes the connection over.
// When the request is not one, it has answered with an error and the
// connection is left to the server.
func acceptWS(w http.ResponseWriter, r *http.Request) (*wsConn, error) {
	key := r.Header.Get("Sec-WebSocket-Key")
	raw, err := base64.StdEncoding.DecodeString(key)
	switch {
	case r.Method != http.MethodGet || !headerHas(r.Header, "Connection", "upgrade") || !headerHas(r.Header, "Upgrade", "websocket"):
		http.Error(w, "This address speaks WebSocket only.", http.StatusBadRequest)
		return nil, errWSProtocol
	case r.Header.Get("Sec-WebSocket-Version") != "13":
		w.Header().Set("Sec-WebSocket-Version", "13")
		http.Error(w, "Unsupported WebSocket version.", http.StatusUpgradeRequired)
		return nil, errWSProtocol
	case err != nil || len(raw) != 16:
		http.Error(w, "Bad WebSocket key.", http.StatusBadRequest)
		return nil, errWSProtocol
	}
	conn, rw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		http.Error(w, "This connection cannot carry a WebSocket.", http.StatusInternalServerError)
		return nil, err
	}
	sum := sha1.Sum([]byte(key + wsGUID))
	conn.SetDeadline(time.Now().Add(writeTimeout))
	_, err = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " +
		base64.StdEncoding.EncodeToString(sum[:]) + "\r\n\r\n")
	if err == nil {
		err = rw.Flush()
	}
	if err != nil {
		conn.Close()
		return nil, err
	}
	conn.SetDeadline(time.Time{})
	// The reader may already hold bytes the browser sent after its request.
	return &wsConn{conn: conn, r: rw.Reader}, nil
}

// Read returns the next message: its kind (wsText or wsBinary) and its
// content. Pings are answered and a close is acknowledged on the way; after
// a close, and on any error, the connection is of no further use. It waits
// until the deadline, when one is given.
func (c *wsConn) Read(deadline time.Time) (kind byte, message []byte, err error) {
	c.conn.SetReadDeadline(deadline)
	var started bool
	for {
		var head [2]byte
		if _, err := io.ReadFull(c.r, head[:]); err != nil {
			return 0, nil, err
		}
		fin, opcode := head[0]&0x80 != 0, head[0]&0x0F
		// No extension was agreed, so the reserved bits mean nothing; and
		// a browser always masks what it sends.
		if head[0]&0x70 != 0 || head[1]&0x80 == 0 {
			c.Close(wsProtocol, "")
			return 0, nil, errWSProtocol
		}
		length := uint64(head[1] & 0x7F)
		control := opcode >= wsClose
		switch {
		case control && (!fin || length > 125):
			c.Close(wsProtocol, "")
			return 0, nil, errWSProtocol
		case length == 126:
			var ext [2]byte
			if _, err := io.ReadFull(c.r, ext[:]); err != nil {
				return 0, nil, err
			}
			length = uint64(binary.BigEndian.Uint16(ext[:]))
		case length == 127:
			var ext [8]byte
			if _, err := io.ReadFull(c.r, ext[:]); err != nil {
				return 0, nil, err
			}
			length = binary.BigEndian.Uint64(ext[:])
		}
		// Checked before anything is allocated for it.
		if length > wsMaxMessage || uint64(len(message))+length > wsMaxMessage {
			c.Close(wsTooBig, "")
			return 0, nil, errors.New("websocket: the message is too long")
		}
		var mask [4]byte
		if _, err := io.ReadFull(c.r, mask[:]); err != nil {
			return 0, nil, err
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(c.r, payload); err != nil {
			return 0, nil, err
		}
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
		switch opcode {
		case wsPing:
			if err := c.Write(wsPong, payload); err != nil {
				return 0, nil, err
			}
		case wsPong:
		case wsClose:
			c.Close(wsNormal, "")
			return 0, nil, io.EOF
		case wsText, wsBinary:
			if started {
				c.Close(wsProtocol, "")
				return 0, nil, errWSProtocol
			}
			started, kind, message = true, opcode, payload
			if fin {
				return kind, message, nil
			}
		case wsContinuation:
			if !started {
				c.Close(wsProtocol, "")
				return 0, nil, errWSProtocol
			}
			message = append(message, payload...)
			if fin {
				return kind, message, nil
			}
		default:
			c.Close(wsProtocol, "")
			return 0, nil, errWSProtocol
		}
	}
}

// Write sends one message. A browser that does not take it within the
// write timeout is given up on.
func (c *wsConn) Write(kind byte, payload []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.closed {
		return net.ErrClosed
	}
	return c.frame(kind, payload)
}

// frame writes one whole, unmasked frame. The caller holds wmu.
func (c *wsConn) frame(kind byte, payload []byte) error {
	head := make([]byte, 2, 10)
	head[0] = 0x80 | kind
	switch n := len(payload); {
	case n <= 125:
		head[1] = byte(n)
	case n <= 0xFFFF:
		head[1] = 126
		head = binary.BigEndian.AppendUint16(head, uint16(n))
	default:
		head[1] = 127
		head = binary.BigEndian.AppendUint64(head, uint64(n))
	}
	c.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	if _, err := c.conn.Write(head); err != nil {
		return err
	}
	_, err := c.conn.Write(payload)
	return err
}

// Close says goodbye with a code and drops the connection. It can be
// called more than once and from any goroutine; a Read that is waiting
// returns.
func (c *wsConn) Close(code uint16, reason string) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	if len(reason) > 100 {
		reason = reason[:100]
	}
	c.frame(wsClose, append(binary.BigEndian.AppendUint16(nil, code), reason...))
	c.conn.Close()
}
