package gatt

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/RolandWarburton/ptouch-print-server/internal/api"
	"github.com/RolandWarburton/ptouch-print-server/internal/proto"
	"github.com/RolandWarburton/ptouch-print-server/internal/ptouch"
)

const testKey = "0123456789abcdef"

type fakePrinter struct{}

func (fakePrinter) Status(context.Context) (ptouch.Status, error) {
	return ptouch.Status{TapeMM: 24, MediaType: 1}, nil
}

func (fakePrinter) Print(context.Context, int, ptouch.RenderFunc) (ptouch.Result, error) {
	return ptouch.Result{}, nil
}

// fakeAdapter records what the peripheral asks BlueZ to do. When autoConfirm
// is set, every indication is confirmed, as a subscribed client would.
type fakeAdapter struct {
	p           *peripheral
	autoConfirm bool

	mu           sync.Mutex
	advertising  bool
	disconnected []dbus.ObjectPath
	frames       chan []byte
}

func (f *fakeAdapter) setAdvertising(on bool) {
	f.mu.Lock()
	f.advertising = on
	f.mu.Unlock()
}

func (f *fakeAdapter) disconnect(dev dbus.ObjectPath) {
	f.mu.Lock()
	f.disconnected = append(f.disconnected, dev)
	f.mu.Unlock()
}

func (f *fakeAdapter) indicate(v []byte) error {
	f.frames <- append([]byte(nil), v...)
	if f.autoConfirm {
		go f.p.confirmed()
	}
	return nil
}

func (f *fakeAdapter) isAdvertising() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.advertising
}

func (f *fakeAdapter) wasDisconnected(dev dbus.ObjectPath) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.disconnected, dev)
}

const (
	devA dbus.ObjectPath = "/org/bluez/hci0/dev_AA_AA_AA_AA_AA_AA"
	devB dbus.ObjectPath = "/org/bluez/hci0/dev_BB_BB_BB_BB_BB_BB"
)

func setup(t *testing.T) (*peripheral, *fakeAdapter) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := api.New(fakePrinter{}, testKey, log)
	ad := &fakeAdapter{autoConfirm: true, advertising: true, frames: make(chan []byte, 100)}
	p := newPeripheral(svc, ad, 2*time.Minute, log)
	ad.p = p
	return p, ad
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// readResponse joins indications until one arrives without the "more" flag.
func readResponse(t *testing.T, ad *fakeAdapter, chunk int) (byte, map[string]any) {
	t.Helper()
	var body []byte
	for {
		select {
		case f := <-ad.frames:
			if len(f) > chunk {
				t.Fatalf("indication of %d bytes, chunk is %d", len(f), chunk)
			}
			body = append(body, f[2:]...)
			if f[1]&1 == 0 {
				var m map[string]any
				if err := json.Unmarshal(body, &m); err != nil {
					t.Fatalf("response isn't JSON: %q", body)
				}
				return f[0], m
			}
		case <-time.After(2 * time.Second):
			t.Fatal("no response")
		}
	}
}

func readInfo(t *testing.T, p *peripheral, dev dbus.ObjectPath, mtu int) map[string]any {
	t.Helper()
	v, derr := p.readInfo(dev, mtu, 0)
	if derr != nil {
		t.Fatal(derr)
	}
	var m map[string]any
	if err := json.Unmarshal(v, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func send(t *testing.T, p *peripheral, dev dbus.ObjectPath, mtu int, id byte, op proto.Op, payload []byte) {
	t.Helper()
	for _, c := range proto.EncodeRequest(id, op, payload, proto.ChunkSize(mtu)) {
		if derr := p.writeRequest(dev, mtu, 0, c); derr != nil {
			t.Fatal(derr)
		}
	}
}

func TestSession(t *testing.T) {
	for _, mtu := range []int{23, 517} {
		p, ad := setup(t)
		p.connected(devA)
		p.setNotifying(true)
		eventually(t, "advertising to stop", func() bool { return !ad.isAdvertising() })

		info := readInfo(t, p, devA, mtu)
		if int(info["chunk"].(float64)) != proto.ChunkSize(mtu) {
			t.Errorf("mtu %d: info chunk %v", mtu, info["chunk"])
		}
		nonce, _ := hex.DecodeString(info["nonce"].(string))

		send(t, p, devA, mtu, 1, proto.OpAuth, api.AuthMAC(testKey, nonce))
		if id, resp := readResponse(t, ad, proto.ChunkSize(mtu)); id != 1 || resp["ok"] != true {
			t.Fatalf("mtu %d: auth response %d %v", mtu, id, resp)
		}
		send(t, p, devA, mtu, 2, proto.OpStatus, nil)
		if id, resp := readResponse(t, ad, proto.ChunkSize(mtu)); id != 2 || resp["tape_mm"] != 24.0 {
			t.Fatalf("mtu %d: status response %d %v", mtu, id, resp)
		}

		p.disconnected(devA)
		if !ad.isAdvertising() {
			t.Errorf("mtu %d: not advertising after disconnect", mtu)
		}
	}
}

func TestInfoReadBlob(t *testing.T) {
	p, _ := setup(t)
	full, derr := p.readInfo(devA, 23, 0)
	if derr != nil {
		t.Fatal(derr)
	}
	rest, derr := p.readInfo(devA, 23, 22)
	if derr != nil || string(rest) != string(full[22:]) {
		t.Errorf("read at offset 22 gave %q, %v", rest, derr)
	}
	if _, derr := p.readInfo(devA, 23, len(full)+1); derr == nil || derr.Name != errInvalidOffset {
		t.Errorf("read past the end gave %v", derr)
	}
}

func TestSecondClientRefused(t *testing.T) {
	p, ad := setup(t)
	p.connected(devA)
	p.connected(devB)
	if !ad.wasDisconnected(devB) {
		t.Error("second client wasn't disconnected")
	}
	if _, derr := p.readInfo(devB, 23, 0); derr == nil || derr.Name != errNotPermitted {
		t.Errorf("second client's read gave %v", derr)
	}
	// The first client is unaffected, and the second one going away changes nothing.
	p.disconnected(devB)
	if p.activeDevice() != devA {
		t.Errorf("active device %q", p.activeDevice())
	}
}

func TestReadBeforeConnectedSignal(t *testing.T) {
	p, ad := setup(t)
	readInfo(t, p, devA, 23)
	p.connected(devA) // the signal arrives late
	if p.activeDevice() != devA || ad.wasDisconnected(devA) {
		t.Errorf("active %q, disconnected %v", p.activeDevice(), ad.disconnected)
	}
}

func TestWriteErrors(t *testing.T) {
	p, _ := setup(t)
	tests := []struct {
		name   string
		offset int
		value  []byte
		want   string
	}{
		{"long write", 5, []byte{1, 2, 0, 0, 0, 0}, errInvalidOffset},
		{"short chunk", 0, []byte{1}, errInvalidLength},
		{"too large", 0, []byte{1, 2, 0xff, 0xff, 0xff, 0x00}, errInvalidLength},
		{"stray continuation", 0, []byte{9, 0, 1, 2}, errFailed},
	}
	for _, tt := range tests {
		derr := p.writeRequest(devA, 23, tt.offset, tt.value)
		if derr == nil || derr.Name != tt.want {
			t.Errorf("%s: got %v, want %s", tt.name, derr, tt.want)
		}
	}
}

func TestNotSubscribed(t *testing.T) {
	p, ad := setup(t)
	send(t, p, devA, 23, 1, proto.OpStatus, nil)
	select {
	case f := <-ad.frames:
		t.Errorf("sent an indication with nobody subscribed: %q", f)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestIdleDisconnect(t *testing.T) {
	p, ad := setup(t)
	now := time.Now()
	p.now = func() time.Time { return now }
	p.connected(devA)
	now = now.Add(time.Minute)
	p.checkIdle()
	if ad.wasDisconnected(devA) {
		t.Fatal("disconnected before the idle timeout")
	}
	now = now.Add(2 * time.Minute)
	p.checkIdle()
	if !ad.wasDisconnected(devA) {
		t.Fatal("not disconnected after the idle timeout")
	}
}

func TestClosingRejectsWrites(t *testing.T) {
	p, ad := setup(t)
	p.connected(devA)
	p.close()
	if derr := p.writeRequest(devA, 23, 0, []byte{1, 2, 0, 0, 0, 0}); derr == nil || derr.Name != errFailed {
		t.Errorf("write while closing gave %v", derr)
	}
	p.disconnected(devA)
	if ad.isAdvertising() {
		t.Error("advertising again while shutting down")
	}
}

// A response being sent stops when its client disconnects, instead of waiting
// for confirmations that will never come.
func TestDisconnectMidResponse(t *testing.T) {
	p, ad := setup(t)
	ad.autoConfirm = false
	p.connected(devA)
	p.setNotifying(true)
	done := make(chan struct{})
	go func() {
		p.respond(p.active, 1, make([]byte, 100)) // several indications at MTU 23
		close(done)
	}()
	<-ad.frames
	p.disconnected(devA)
	p.confirmed()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("respond didn't stop after the disconnect")
	}
	if len(ad.frames) != 0 {
		t.Errorf("%d more indications sent after the disconnect", len(ad.frames))
	}
}
