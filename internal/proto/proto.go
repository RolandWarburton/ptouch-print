// Package proto frames requests and responses for the Bluetooth GATT API.
// A message can be bigger than one attribute write, so requests arrive as
// chunks written to the request characteristic and responses leave as chunks
// sent as indications. The package knows nothing about BlueZ or the printer.
package proto

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

const (
	Version = 1

	// MaxMessage is the largest request payload accepted, in bytes. It fits a
	// max_label_dots label even as an uncompressed 1-bit image.
	MaxMessage = 64 << 10

	// MinChunk and MaxChunk bound the size of one attribute value: the
	// default ATT MTU of 23 less 3 bytes of ATT header, and the largest
	// attribute value ATT allows.
	MinChunk = 20
	MaxChunk = 512

	// PartTimeout is how long a partly received request waits for its next
	// chunk before it is thrown away.
	PartTimeout = 10 * time.Second

	startHeader    = 6 // req_id, op, uint32 LE length
	contHeader     = 2 // req_id, 0x00
	responseHeader = 2 // req_id, flags
)

// GATT UUIDs. Clients filter on ServiceUUID when scanning.
const (
	ServiceUUID  = "c30d0001-ccea-4d82-aa2f-1ed9328fb057"
	InfoUUID     = "c30d0002-ccea-4d82-aa2f-1ed9328fb057"
	RequestUUID  = "c30d0003-ccea-4d82-aa2f-1ed9328fb057"
	ResponseUUID = "c30d0004-ccea-4d82-aa2f-1ed9328fb057"
)

// Response flags.
const flagMore = 0x01

type Op byte

const (
	opContinue Op = 0x00 // marks a continuation chunk, never a request
	OpAuth     Op = 0x01
	OpStatus   Op = 0x02
	OpBarcode  Op = 0x03
	OpImage    Op = 0x04
)

func (o Op) String() string {
	switch o {
	case OpAuth:
		return "auth"
	case OpStatus:
		return "status"
	case OpBarcode:
		return "barcode"
	case OpImage:
		return "image"
	}
	return fmt.Sprintf("op 0x%02x", byte(o))
}

var (
	ErrShortChunk = errors.New("chunk is shorter than its header")
	ErrNoRequest  = errors.New("continuation chunk with no matching request in progress")
	ErrTooLarge   = fmt.Errorf("message is larger than %d bytes", MaxMessage)
	ErrOverrun    = errors.New("chunk runs past the message length")
)

// Request is a complete request message.
type Request struct {
	ID      byte
	Op      Op
	Payload []byte
}

// Reassembler rebuilds requests from the chunks written by one client. Any
// error throws away the request in progress.
type Reassembler struct {
	cur  *Request
	want int
	last time.Time
}

// Add takes one chunk. It returns the request once its last chunk arrives, or
// nil while more chunks are expected.
func (r *Reassembler) Add(chunk []byte, now time.Time) (*Request, error) {
	if len(chunk) < contHeader {
		r.Reset()
		return nil, ErrShortChunk
	}
	id, op := chunk[0], Op(chunk[1])
	if op != opContinue {
		// A new request replaces one left unfinished.
		r.Reset()
		if len(chunk) < startHeader {
			return nil, ErrShortChunk
		}
		n := binary.LittleEndian.Uint32(chunk[2:startHeader])
		if n > MaxMessage {
			return nil, ErrTooLarge
		}
		r.cur, r.want = &Request{ID: id, Op: op, Payload: make([]byte, 0, n)}, int(n)
		return r.append(chunk[startHeader:], now)
	}
	if r.cur == nil || r.cur.ID != id || now.Sub(r.last) > PartTimeout {
		r.Reset()
		return nil, ErrNoRequest
	}
	return r.append(chunk[contHeader:], now)
}

func (r *Reassembler) append(b []byte, now time.Time) (*Request, error) {
	if len(r.cur.Payload)+len(b) > r.want {
		r.Reset()
		return nil, ErrOverrun
	}
	r.cur.Payload = append(r.cur.Payload, b...)
	r.last = now
	if len(r.cur.Payload) < r.want {
		return nil, nil
	}
	req := r.cur
	r.Reset()
	return req, nil
}

// Reset throws away any request in progress.
func (r *Reassembler) Reset() {
	r.cur, r.want = nil, 0
}

// ChunkSize is the largest attribute value that fits in one ATT packet for
// the given MTU.
func ChunkSize(mtu int) int {
	return min(max(mtu-3, MinChunk), MaxChunk)
}

// EncodeRequest splits a request into chunks of at most chunk bytes. It is
// what a client does, and is here for tests and Go clients.
func EncodeRequest(id byte, op Op, payload []byte, chunk int) [][]byte {
	if chunk < MinChunk || op == opContinue {
		panic("proto: bad EncodeRequest arguments")
	}
	first := make([]byte, startHeader, chunk)
	first[0], first[1] = id, byte(op)
	binary.LittleEndian.PutUint32(first[2:], uint32(len(payload)))
	n := min(len(payload), chunk-startHeader)
	chunks := [][]byte{append(first, payload[:n]...)}
	for rest := payload[n:]; len(rest) > 0; rest = rest[n:] {
		n = min(len(rest), chunk-contHeader)
		chunks = append(chunks, append([]byte{id, byte(opContinue)}, rest[:n]...))
	}
	return chunks
}

// SplitResponse splits a response body into indications of at most chunk
// bytes. Every one but the last has the "more" flag set.
func SplitResponse(id byte, body []byte, chunk int) [][]byte {
	if chunk < MinChunk {
		panic("proto: chunk smaller than MinChunk")
	}
	per := chunk - responseHeader
	var frames [][]byte
	for {
		n := min(len(body), per)
		var flags byte
		if n < len(body) {
			flags = flagMore
		}
		frames = append(frames, append([]byte{id, flags}, body[:n]...))
		body = body[n:]
		if flags == 0 {
			return frames
		}
	}
}
