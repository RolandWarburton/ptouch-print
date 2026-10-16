// Package gatt serves the api package over Bluetooth LE through BlueZ. It
// registers the GATT service, the advertisement and a pairing agent over
// D-Bus, tracks the one connected client, and carries chunks between BlueZ
// and api.Conn.
package gatt

import (
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/RolandWarburton/ptouch-print-server/internal/api"
	"github.com/RolandWarburton/ptouch-print-server/internal/proto"
)

// confirmTimeout is how long to wait for a client to confirm an indication
// before giving up on the rest of the response.
const confirmTimeout = 5 * time.Second

// BlueZ error names returned from ReadValue and WriteValue. BlueZ turns them
// into ATT errors.
const (
	errFailed        = "org.bluez.Error.Failed"
	errInvalidOffset = "org.bluez.Error.InvalidOffset"
	errInvalidLength = "org.bluez.Error.InvalidValueLength"
	errNotPermitted  = "org.bluez.Error.NotPermitted"
	errRejected      = "org.bluez.Error.Rejected"
)

func bluezError(name, msg string) *dbus.Error {
	return dbus.NewError(name, []any{msg})
}

// adapter is what the peripheral needs from BlueZ. *bluez implements it; tests
// use a fake.
type adapter interface {
	// setAdvertising registers or unregisters the advertisement. It must be
	// safe to call with the current state.
	setAdvertising(on bool)
	// disconnect drops the connection to a device.
	disconnect(dev dbus.ObjectPath)
	// indicate sets the response characteristic's value, which makes BlueZ
	// send it as an indication to subscribed clients.
	indicate(value []byte) error
}

// client is the connected device.
type client struct {
	dev        dbus.ObjectPath
	conn       *api.Conn
	mtu        int
	lastActive time.Time
}

// peripheral holds the connection state. The D-Bus objects call into it from
// whatever goroutine BlueZ's calls arrive on.
type peripheral struct {
	svc  *api.Service
	ad   adapter
	log  *slog.Logger
	idle time.Duration
	now  func() time.Time

	mu        sync.Mutex
	active    *client
	notifying bool
	closing   bool

	advMu   sync.Mutex    // serializes syncAdvertising
	sendMu  sync.Mutex    // one response at a time goes out as indications
	confirm chan struct{} // signalled by Confirm()
}

func newPeripheral(svc *api.Service, ad adapter, idle time.Duration, log *slog.Logger) *peripheral {
	return &peripheral{svc: svc, ad: ad, log: log, idle: idle, now: time.Now, confirm: make(chan struct{}, 1)}
}

// connected is called when a device connects. Only the first device gets a
// session; any other is dropped, because BlueZ would send it the first one's
// indications.
func (p *peripheral) connected(dev dbus.ObjectPath) {
	p.mu.Lock()
	_, ok := p.claim(dev)
	p.mu.Unlock()
	if !ok {
		p.log.Warn("refusing a second client", "device", dev)
		p.ad.disconnect(dev)
	}
}

// claim returns the session for dev, starting one if nobody is connected. It
// is called with p.mu held.
func (p *peripheral) claim(dev dbus.ObjectPath) (*client, bool) {
	if p.active != nil {
		return p.active, p.active.dev == dev
	}
	c := &client{dev: dev, mtu: 23, lastActive: p.now()}
	c.conn = p.svc.Connect(string(dev), func(id byte, body []byte) { p.respond(c, id, body) })
	p.active = c
	p.log.Info("client connected", "device", dev)
	go p.syncAdvertising() // not with p.mu held
	return c, true
}

// disconnected is called when a device disconnects.
func (p *peripheral) disconnected(dev dbus.ObjectPath) {
	p.mu.Lock()
	c := p.active
	if c == nil || c.dev != dev {
		p.mu.Unlock()
		return
	}
	p.active = nil
	p.mu.Unlock()

	c.conn.Close()
	p.log.Info("client disconnected", "device", dev)
	p.syncAdvertising()
}

// syncAdvertising advertises only while nobody is connected. It reads the
// state when it runs, so calls that race each other still end up right.
func (p *peripheral) syncAdvertising() {
	p.advMu.Lock()
	defer p.advMu.Unlock()
	p.mu.Lock()
	on := p.active == nil && !p.closing
	p.mu.Unlock()
	p.ad.setAdvertising(on)
}

// session finds the session for a ReadValue or WriteValue from dev. A call can
// arrive before the Connected signal, so it may start the session itself.
func (p *peripheral) session(dev dbus.ObjectPath, mtu int) (*client, *dbus.Error) {
	if dev == "" {
		return nil, bluezError(errFailed, "BlueZ didn't say which device this is")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	c, ok := p.claim(dev)
	if !ok {
		go p.ad.disconnect(dev)
		return nil, bluezError(errNotPermitted, "another client is connected")
	}
	if mtu > 0 {
		c.mtu = mtu
	}
	return c, nil
}

// readInfo answers a read of the info characteristic. offset is set when the
// value is longer than one packet and the client reads the rest (Read Blob).
func (p *peripheral) readInfo(dev dbus.ObjectPath, mtu, offset int) ([]byte, *dbus.Error) {
	c, derr := p.session(dev, mtu)
	if derr != nil {
		return nil, derr
	}
	p.mu.Lock()
	chunk := proto.ChunkSize(c.mtu)
	p.mu.Unlock()
	v := c.conn.Info(chunk)
	if offset > len(v) {
		return nil, bluezError(errInvalidOffset, "offset is past the end of the value")
	}
	return v[offset:], nil
}

// writeRequest takes one chunk written to the request characteristic.
func (p *peripheral) writeRequest(dev dbus.ObjectPath, mtu, offset int, value []byte) *dbus.Error {
	if offset != 0 {
		return bluezError(errInvalidOffset, "long writes aren't supported, write at most info.chunk bytes")
	}
	c, derr := p.session(dev, mtu)
	if derr != nil {
		return derr
	}
	p.mu.Lock()
	if p.closing {
		p.mu.Unlock()
		return bluezError(errFailed, "the server is shutting down")
	}
	c.lastActive = p.now()
	p.mu.Unlock()

	err := c.conn.Write(value)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, proto.ErrShortChunk), errors.Is(err, proto.ErrTooLarge), errors.Is(err, proto.ErrOverrun):
		return bluezError(errInvalidLength, err.Error())
	default:
		return bluezError(errFailed, err.Error())
	}
}

func (p *peripheral) setNotifying(on bool) {
	p.mu.Lock()
	p.notifying = on
	p.mu.Unlock()
}

// confirmed is called when the client confirms an indication.
func (p *peripheral) confirmed() {
	select {
	case p.confirm <- struct{}{}:
	default:
	}
}

// respond sends a response body to c as indications, waiting for each one to
// be confirmed before sending the next.
func (p *peripheral) respond(c *client, id byte, body []byte) {
	p.sendMu.Lock()
	defer p.sendMu.Unlock()

	p.mu.Lock()
	chunk := proto.ChunkSize(c.mtu)
	p.mu.Unlock()
	for i, frame := range proto.SplitResponse(id, body, chunk) {
		p.mu.Lock()
		current, notifying := p.active == c, p.notifying
		p.mu.Unlock()
		if !current {
			return // disconnected; nobody to send to
		}
		if !notifying {
			p.log.Warn("dropping a response: the client isn't subscribed to indications", "device", c.dev, "req_id", id)
			return
		}

		select { // throw away a late confirmation from an earlier indication
		case <-p.confirm:
		default:
		}
		if err := p.ad.indicate(frame); err != nil {
			p.log.Warn("sending an indication", "device", c.dev, "err", err)
			return
		}
		select {
		case <-p.confirm:
		case <-time.After(confirmTimeout):
			p.log.Warn("indication not confirmed, dropping the rest of the response",
				"device", c.dev, "req_id", id, "chunk", i)
			return
		}
	}
	p.mu.Lock()
	c.lastActive = p.now()
	p.mu.Unlock()
}

// checkIdle disconnects the client if it hasn't written anything for the idle
// timeout.
func (p *peripheral) checkIdle() {
	p.mu.Lock()
	c := p.active
	idle := c != nil && p.now().Sub(c.lastActive) > p.idle
	p.mu.Unlock()
	if idle {
		p.log.Info("disconnecting an idle client", "device", c.dev)
		p.ad.disconnect(c.dev)
	}
}

// close stops taking writes. Requests already running still get answered.
func (p *peripheral) close() {
	p.mu.Lock()
	p.closing = true
	p.mu.Unlock()
}

// activeDevice returns the connected device, if any.
func (p *peripheral) activeDevice() dbus.ObjectPath {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.active == nil {
		return ""
	}
	return p.active.dev
}
