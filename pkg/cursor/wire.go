// Portions derived from yetone/magpie, commit 07381545205b5f47ab448a1c6a307ee86c279fa8.
// Copyright (c) 2026 yetone. MIT license; see LICENSE.magpie.
// Local experimental Cursor protocol implementation; not an official Cursor API.
package cursor

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"io"
	"math"
)

type pb []byte

func (b pb) tag(num, wire int) pb { return binary.AppendUvarint(b, uint64(num<<3|wire)) }

func (b pb) varint(num int, v uint64) pb { return binary.AppendUvarint(b.tag(num, 0), v) }

func (b pb) bytes(num int, v []byte) pb {
	return append(binary.AppendUvarint(b.tag(num, 2), uint64(len(v))), v...)
}

func (b pb) str(num int, v string) pb { return b.bytes(num, []byte(v)) }

func (b pb) double(num int, v float64) pb {
	return binary.LittleEndian.AppendUint64(b.tag(num, 1), math.Float64bits(v))
}

// pbField is one field of a message being read.
type pbField struct {
	num  int
	wire int
	n    uint64 // a varint or fixed value
	data []byte // a length-delimited one
}

// pbFields reads a message's fields; a malformed tail is dropped.
func pbFields(b []byte) []pbField {
	var out []pbField
	for len(b) > 0 {
		key, n := binary.Uvarint(b)
		if n <= 0 {
			break
		}
		b = b[n:]
		f := pbField{num: int(key >> 3), wire: int(key & 7)}
		switch f.wire {
		case 0:
			v, n := binary.Uvarint(b)
			if n <= 0 {
				return out
			}
			f.n, b = v, b[n:]
		case 1:
			if len(b) < 8 {
				return out
			}
			f.n, b = binary.LittleEndian.Uint64(b), b[8:]
		case 2:
			l, n := binary.Uvarint(b)
			if n <= 0 || uint64(len(b)-n) < l {
				return out
			}
			f.data, b = b[n:n+int(l)], b[n+int(l):]
		case 5:
			if len(b) < 4 {
				return out
			}
			f.n, b = uint64(binary.LittleEndian.Uint32(b)), b[4:]
		default:
			return out
		}
		out = append(out, f)
	}
	return out
}

// connectFrame wraps a message as the one frame of a Connect stream.
func connectFrame(msg []byte) []byte {
	out := make([]byte, 5, 5+len(msg))
	binary.BigEndian.PutUint32(out[1:], uint32(len(msg)))
	return append(out, msg...)
}

// connectMsg is one frame of a Connect stream: a message, or the stream's
// end with its JSON.
type connectMsg struct {
	end  bool
	data []byte
}

func readConnectFrame(r *bufio.Reader) (connectMsg, error) {
	var head [5]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return connectMsg{}, err
	}
	n := binary.BigEndian.Uint32(head[1:])
	if n > 8<<20 {
		return connectMsg{}, errors.New("a malformed stream")
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(r, data); err != nil {
		return connectMsg{}, err
	}
	if head[0]&1 != 0 {
		zr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return connectMsg{}, err
		}
		defer zr.Close()
		if data, err = io.ReadAll(io.LimitReader(zr, (8<<20)+1)); err != nil {
			return connectMsg{}, err
		}
	}
	if len(data) > 8<<20 {
		return connectMsg{}, errors.New("oversized Cursor frame")
	}
	return connectMsg{end: head[0]&2 != 0, data: data}, nil
}
