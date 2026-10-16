package proto

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func payload(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i * 7)
	}
	return b
}

// feed adds each chunk in turn and returns the request the last one completes.
func feed(t *testing.T, r *Reassembler, chunks [][]byte) *Request {
	t.Helper()
	for i, c := range chunks {
		req, err := r.Add(c, t0)
		if err != nil {
			t.Fatalf("chunk %d: %v", i, err)
		}
		if (req != nil) != (i == len(chunks)-1) {
			t.Fatalf("chunk %d of %d: request complete = %v", i, len(chunks), req != nil)
		}
		if req != nil {
			return req
		}
	}
	return nil
}

func TestRoundTrip(t *testing.T) {
	for _, chunk := range []int{MinChunk, 21, 182, MaxChunk} {
		for _, n := range []int{0, 1, chunk - startHeader, chunk - startHeader + 1, 5000, MaxMessage} {
			var r Reassembler
			want := payload(n)
			chunks := EncodeRequest(9, OpImage, want, chunk)
			for i, c := range chunks {
				if len(c) > chunk {
					t.Fatalf("chunk %d is %d bytes, limit %d", i, len(c), chunk)
				}
			}
			req := feed(t, &r, chunks)
			if req.ID != 9 || req.Op != OpImage || !bytes.Equal(req.Payload, want) {
				t.Errorf("chunk %d, payload %d: got id %d op %v, %d bytes", chunk, n, req.ID, req.Op, len(req.Payload))
			}
		}
	}
}

func TestNewRequestReplacesUnfinished(t *testing.T) {
	var r Reassembler
	first := EncodeRequest(1, OpImage, payload(100), MinChunk)
	if req, err := r.Add(first[0], t0); req != nil || err != nil {
		t.Fatalf("got %v, %v", req, err)
	}
	req := feed(t, &r, EncodeRequest(2, OpStatus, nil, MinChunk))
	if req.ID != 2 || req.Op != OpStatus {
		t.Errorf("got %+v", req)
	}
	if _, err := r.Add(first[1], t0); !errors.Is(err, ErrNoRequest) {
		t.Errorf("continuing the replaced request: %v, want ErrNoRequest", err)
	}
}

func TestReassemblyErrors(t *testing.T) {
	chunks := EncodeRequest(1, OpImage, payload(100), MinChunk)
	tests := []struct {
		name  string
		setup [][]byte // chunks that should be accepted first
		chunk []byte
		now   time.Time
		want  error
	}{
		{"empty", nil, []byte{}, t0, ErrShortChunk},
		{"one byte", nil, []byte{1}, t0, ErrShortChunk},
		{"start without length", nil, []byte{1, byte(OpStatus), 0, 0}, t0, ErrShortChunk},
		{"too large", nil, []byte{1, byte(OpImage), 0x01, 0x00, 0x01, 0x00}, t0, ErrTooLarge},
		{"longer than declared", nil, []byte{1, byte(OpBarcode), 1, 0, 0, 0, 'a', 'b'}, t0, ErrOverrun},
		{"continuation first", nil, chunks[1], t0, ErrNoRequest},
		{"wrong id", chunks[:1], append([]byte{2, 0}, chunks[1][2:]...), t0, ErrNoRequest},
		{"too slow", chunks[:1], chunks[1], t0.Add(PartTimeout + time.Second), ErrNoRequest},
	}
	for _, tt := range tests {
		var r Reassembler
		for _, c := range tt.setup {
			if _, err := r.Add(c, t0); err != nil {
				t.Fatalf("%s: setup: %v", tt.name, err)
			}
		}
		if req, err := r.Add(tt.chunk, tt.now); req != nil || !errors.Is(err, tt.want) {
			t.Errorf("%s: got %v, %v; want %v", tt.name, req, err, tt.want)
		}
		if r.cur != nil {
			t.Errorf("%s: request still in progress after an error", tt.name)
		}
	}
}

func TestSplitResponse(t *testing.T) {
	for _, n := range []int{0, 1, 18, 19, 500} {
		body := payload(n)
		frames := SplitResponse(4, body, MinChunk)
		var got []byte
		for i, f := range frames {
			last := i == len(frames)-1
			if len(f) > MinChunk || f[0] != 4 || (f[1] == flagMore) == last {
				t.Fatalf("%d bytes: bad frame %d of %d: % x", n, i, len(frames), f)
			}
			got = append(got, f[responseHeader:]...)
		}
		if !bytes.Equal(got, body) {
			t.Errorf("%d bytes: body doesn't survive the round trip", n)
		}
	}
}

func TestChunkSize(t *testing.T) {
	for mtu, want := range map[int]int{0: MinChunk, 23: 20, 185: 182, 517: 512, 1000: MaxChunk} {
		if got := ChunkSize(mtu); got != want {
			t.Errorf("ChunkSize(%d) = %d, want %d", mtu, got, want)
		}
	}
}
