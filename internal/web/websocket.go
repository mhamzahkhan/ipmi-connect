package web

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

	OpContinuation = 0x0
	OpText         = 0x1
	OpBinary       = 0x2
	OpClose        = 0x8
	OpPing         = 0x9
	OpPong         = 0xA
)

// WSConn is a minimal RFC 6455 WebSocket connection.
type WSConn struct {
	conn   net.Conn
	reader *bufio.Reader
	mu     sync.Mutex
	closed bool
}

// Upgrade upgrades an HTTP connection to a WebSocket connection.
func Upgrade(w http.ResponseWriter, r *http.Request) (*WSConn, error) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return nil, errors.New("not a websocket upgrade request")
	}

	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		return nil, errors.New("missing Sec-WebSocket-Key")
	}

	// Compute accept key: base64(sha1(key + GUID))
	h := sha1.New()
	h.Write([]byte(key + wsGUID))
	acceptKey := base64.StdEncoding.EncodeToString(h.Sum(nil))

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		return nil, errors.New("response writer does not support hijacking")
	}

	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return nil, err
	}

	// Send handshake response
	resp := fmt.Sprintf("HTTP/1.1 101 Switching Protocols\r\n"+
		"Upgrade: websocket\r\n"+
		"Connection: Upgrade\r\n"+
		"Sec-WebSocket-Accept: %s\r\n\r\n", acceptKey)

	if _, err := conn.Write([]byte(resp)); err != nil {
		conn.Close()
		return nil, err
	}

	return &WSConn{
		conn:   conn,
		reader: rw.Reader,
	}, nil
}

// Close closes the connection.
func (ws *WSConn) Close() error {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if ws.closed {
		return nil
	}
	ws.closed = true
	return ws.conn.Close()
}

// WriteMessage writes a text or binary WebSocket frame to the client.
func (ws *WSConn) WriteMessage(opCode byte, data []byte) error {
	ws.mu.Lock()
	defer ws.mu.Unlock()

	if ws.closed {
		return errors.New("connection closed")
	}

	length := len(data)
	var header []byte

	// FIN bit = 1, Opcode
	b0 := byte(0x80) | (opCode & 0x0F)

	if length <= 125 {
		header = []byte{b0, byte(length)}
	} else if length <= 65535 {
		header = make([]byte, 4)
		header[0] = b0
		header[1] = 126
		binary.BigEndian.PutUint16(header[2:4], uint16(length))
	} else {
		header = make([]byte, 10)
		header[0] = b0
		header[1] = 127
		binary.BigEndian.PutUint64(header[2:10], uint64(length))
	}

	_ = ws.conn.SetWriteDeadline(time.Now().Add(1 * time.Second))
	if _, err := ws.conn.Write(header); err != nil {
		return err
	}

	if length > 0 {
		if _, err := ws.conn.Write(data); err != nil {
			return err
		}
	}

	return nil
}

// ReadMessage reads the next WebSocket message frame from the client.
func (ws *WSConn) ReadMessage() (byte, []byte, error) {
	for {
		b0, err := ws.reader.ReadByte()
		if err != nil {
			return 0, nil, err
		}

		opCode := b0 & 0x0F
		b1, err := ws.reader.ReadByte()
		if err != nil {
			return 0, nil, err
		}

		isMasked := (b1 & 0x80) != 0
		payloadLen := uint64(b1 & 0x7F)

		switch payloadLen {
		case 126:
			var l uint16
			if err := binary.Read(ws.reader, binary.BigEndian, &l); err != nil {
				return 0, nil, err
			}
			payloadLen = uint64(l)
		case 127:
			if err := binary.Read(ws.reader, binary.BigEndian, &payloadLen); err != nil {
				return 0, nil, err
			}
		}

		var maskKey [4]byte
		if isMasked {
			if _, err := io.ReadFull(ws.reader, maskKey[:]); err != nil {
				return 0, nil, err
			}
		}

		payload := make([]byte, payloadLen)
		if _, err := io.ReadFull(ws.reader, payload); err != nil {
			return 0, nil, err
		}

		if isMasked {
			for i := uint64(0); i < payloadLen; i++ {
				payload[i] ^= maskKey[i%4]
			}
		}

		switch opCode {
		case OpPing:
			_ = ws.WriteMessage(OpPong, payload)
		case OpClose:
			_ = ws.Close()
			return OpClose, nil, io.EOF
		case OpText, OpBinary:
			return opCode, payload, nil
		}
	}
}
