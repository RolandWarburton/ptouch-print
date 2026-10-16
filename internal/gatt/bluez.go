package gatt

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/RolandWarburton/ptouch-print-server/internal/api"
	"github.com/RolandWarburton/ptouch-print-server/internal/proto"
)

const (
	bluezName = "org.bluez"

	ifaceAdapter     = "org.bluez.Adapter1"
	ifaceDevice      = "org.bluez.Device1"
	ifaceGattManager = "org.bluez.GattManager1"
	ifaceGattService = "org.bluez.GattService1"
	ifaceGattChar    = "org.bluez.GattCharacteristic1"
	ifaceAdvManager  = "org.bluez.LEAdvertisingManager1"
	ifaceAdv         = "org.bluez.LEAdvertisement1"
	ifaceAgentMgr    = "org.bluez.AgentManager1"
	ifaceAgent       = "org.bluez.Agent1"
	ifaceProps       = "org.freedesktop.DBus.Properties"
	ifaceObjMgr      = "org.freedesktop.DBus.ObjectManager"

	appPath      dbus.ObjectPath = "/org/ptouch/gatt"
	servicePath  dbus.ObjectPath = appPath + "/service0"
	infoPath     dbus.ObjectPath = servicePath + "/char0"
	requestPath  dbus.ObjectPath = servicePath + "/char1"
	responsePath dbus.ObjectPath = servicePath + "/char2"
	advPath      dbus.ObjectPath = "/org/ptouch/advertisement0"
	agentPath    dbus.ObjectPath = "/org/ptouch/agent"

	callTimeout = 10 * time.Second
	idleCheck   = 10 * time.Second
)

type Config struct {
	Adapter     string        // e.g. "hci0"
	Name        string        // advertised name
	IdleTimeout time.Duration // disconnect a client that writes nothing for this long
	// ShutdownTimeout bounds how long Serve waits for a running request to be
	// answered once ctx is done.
	ShutdownTimeout time.Duration
}

// Serve registers the GATT service with BlueZ and answers clients until ctx
// is done, then lets a running request finish and unregisters. It returns an
// error if setup fails or bluetoothd goes away.
func Serve(ctx context.Context, cfg Config, svc *api.Service, log *slog.Logger) error {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return fmt.Errorf("connecting to the system bus: %w", err)
	}
	defer conn.Close()

	b := &bluez{conn: conn, adapter: dbus.ObjectPath("/org/bluez/" + cfg.Adapter), name: cfg.Name, log: log}
	p := newPeripheral(svc, b, cfg.IdleTimeout, log)

	// Subscribe before setting up so no connection is missed.
	signals := make(chan *dbus.Signal, 32)
	conn.Signal(signals)
	if err := conn.AddMatchSignal(dbus.WithMatchInterface(ifaceProps), dbus.WithMatchMember("PropertiesChanged"),
		dbus.WithMatchPathNamespace(b.adapter)); err != nil {
		return fmt.Errorf("subscribing to device changes: %w", err)
	}
	if err := conn.AddMatchSignal(dbus.WithMatchSender("org.freedesktop.DBus"), dbus.WithMatchMember("NameOwnerChanged"),
		dbus.WithMatchArg(0, bluezName)); err != nil {
		return fmt.Errorf("watching bluetoothd: %w", err)
	}

	if err := b.setup(p); err != nil {
		b.teardown()
		return err
	}
	log.Info("advertising", "name", cfg.Name, "adapter", cfg.Adapter, "address", b.address)

	tick := time.NewTicker(idleCheck)
	defer tick.Stop()
	for {
		select {
		case sig := <-signals:
			if err := b.handleSignal(p, sig); err != nil {
				return err // bluetoothd is gone, so there is nothing to unregister
			}
		case <-tick.C:
			p.checkIdle()
		case <-ctx.Done():
			p.close()
			b.setAdvertising(false)
			waitTimeout(svc.Wait, cfg.ShutdownTimeout)
			if dev := p.activeDevice(); dev != "" {
				b.disconnect(dev)
			}
			b.teardown()
			return nil
		}
	}
}

func waitTimeout(wait func(), d time.Duration) {
	done := make(chan struct{})
	go func() { wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
	}
}

// bluez is the D-Bus side: the exported objects and calls into bluetoothd.
type bluez struct {
	conn    *dbus.Conn
	adapter dbus.ObjectPath
	name    string
	address string
	log     *slog.Logger

	advMu      sync.Mutex
	advertised bool

	app      *application
	response *propObject
	agentUp  bool
	appUp    bool
}

func (b *bluez) call(path dbus.ObjectPath, method string, args ...any) *dbus.Call {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return b.conn.Object(bluezName, path).CallWithContext(ctx, method, 0, args...)
}

func (b *bluez) setProp(path dbus.ObjectPath, iface, name string, v any) error {
	return b.call(path, ifaceProps+".Set", iface, name, dbus.MakeVariant(v)).Err
}

func (b *bluez) setup(p *peripheral) error {
	var addr string
	if err := b.call(b.adapter, ifaceProps+".Get", ifaceAdapter, "Address").Store(&addr); err != nil {
		return fmt.Errorf("adapter %s not found (is bluetoothd running?): %w", b.adapter, err)
	}
	b.address = addr
	if err := b.setProp(b.adapter, ifaceAdapter, "Powered", true); err != nil {
		return fmt.Errorf("powering on %s (is it blocked by rfkill?): %w", b.adapter, err)
	}
	// The GAP device name is the adapter's alias.
	if err := b.setProp(b.adapter, ifaceAdapter, "Alias", b.name); err != nil {
		return fmt.Errorf("setting the adapter name: %w", err)
	}
	if err := b.setProp(b.adapter, ifaceAdapter, "Pairable", true); err != nil {
		return fmt.Errorf("making the adapter pairable: %w", err)
	}

	if err := b.export(p); err != nil {
		return err
	}

	// Without an agent, bluetoothd refuses pairing from a headless device.
	if err := b.call("/org/bluez", ifaceAgentMgr+".RegisterAgent", agentPath, "NoInputNoOutput").Err; err != nil {
		return fmt.Errorf("registering the pairing agent: %w", err)
	}
	b.agentUp = true
	if err := b.call("/org/bluez", ifaceAgentMgr+".RequestDefaultAgent", agentPath).Err; err != nil {
		return fmt.Errorf("making the pairing agent the default: %w", err)
	}
	if err := b.call(b.adapter, ifaceGattManager+".RegisterApplication", appPath, map[string]dbus.Variant{}).Err; err != nil {
		return fmt.Errorf("registering the GATT service: %w", err)
	}
	b.appUp = true

	// Start from a clean state: drop anything left connected from before.
	for _, dev := range b.connectedDevices() {
		b.log.Info("disconnecting a device connected before startup", "device", dev)
		b.disconnect(dev)
	}
	b.setAdvertising(true)
	b.advMu.Lock()
	ok := b.advertised
	b.advMu.Unlock()
	if !ok {
		return fmt.Errorf("couldn't start advertising, see the log above")
	}
	return nil
}

// teardown unregisters everything setup registered. Errors are ignored: this
// runs on the way out.
func (b *bluez) teardown() {
	b.setAdvertising(false)
	if b.appUp {
		b.call(b.adapter, ifaceGattManager+".UnregisterApplication", appPath)
	}
	if b.agentUp {
		b.call("/org/bluez", ifaceAgentMgr+".UnregisterAgent", agentPath)
	}
}

func (b *bluez) connectedDevices() []dbus.ObjectPath {
	var objs map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	if err := b.call("/", ifaceObjMgr+".GetManagedObjects").Store(&objs); err != nil {
		b.log.Warn("listing devices", "err", err)
		return nil
	}
	var devs []dbus.ObjectPath
	for path, ifaces := range objs {
		dev, ok := ifaces[ifaceDevice]
		if !ok || !strings.HasPrefix(string(path), string(b.adapter)+"/") {
			continue
		}
		if c, ok := dev["Connected"].Value().(bool); ok && c {
			devs = append(devs, path)
		}
	}
	return devs
}

// handleSignal passes device connections and disconnections to p. It returns
// an error if bluetoothd has gone away.
func (b *bluez) handleSignal(p *peripheral, sig *dbus.Signal) error {
	switch sig.Name {
	case "org.freedesktop.DBus.NameOwnerChanged":
		if len(sig.Body) == 3 && sig.Body[0] == bluezName && sig.Body[2] == "" {
			return fmt.Errorf("bluetoothd stopped")
		}
	case ifaceProps + ".PropertiesChanged":
		if len(sig.Body) < 2 || sig.Body[0] != ifaceDevice {
			return nil
		}
		changed, _ := sig.Body[1].(map[string]dbus.Variant)
		v, ok := changed["Connected"]
		if !ok {
			return nil
		}
		if c, _ := v.Value().(bool); c {
			p.connected(sig.Path)
		} else {
			p.disconnected(sig.Path)
		}
	}
	return nil
}

func (b *bluez) setAdvertising(on bool) {
	b.advMu.Lock()
	defer b.advMu.Unlock()
	if on == b.advertised {
		return
	}
	var err error
	if on {
		err = b.call(b.adapter, ifaceAdvManager+".RegisterAdvertisement", advPath, map[string]dbus.Variant{}).Err
	} else {
		err = b.call(b.adapter, ifaceAdvManager+".UnregisterAdvertisement", advPath).Err
	}
	if err != nil {
		b.log.Warn("changing advertising", "on", on, "err", err)
		return
	}
	b.advertised = on
}

func (b *bluez) disconnect(dev dbus.ObjectPath) {
	if err := b.call(dev, ifaceDevice+".Disconnect").Err; err != nil {
		b.log.Warn("disconnecting", "device", dev, "err", err)
	}
}

func (b *bluez) indicate(value []byte) error {
	return b.response.set(ifaceGattChar, "Value", value)
}

// export puts the GATT application, the advertisement and the agent on the
// bus.
func (b *bluez) export(p *peripheral) error {
	svc := newPropObject(b.conn, servicePath, ifaceGattService, map[string]any{
		"UUID":    proto.ServiceUUID,
		"Primary": true,
	})
	char := func(path dbus.ObjectPath, uuid string, flags ...string) *propObject {
		return newPropObject(b.conn, path, ifaceGattChar, map[string]any{
			"UUID":    uuid,
			"Service": servicePath,
			"Flags":   flags,
		})
	}
	info := char(infoPath, proto.InfoUUID, "encrypt-read")
	request := char(requestPath, proto.RequestUUID, "encrypt-write")
	b.response = char(responsePath, proto.ResponseUUID, "indicate")
	b.response.props["Value"] = dbus.MakeVariant([]byte{})
	adv := newPropObject(b.conn, advPath, ifaceAdv, map[string]any{
		"Type":         "peripheral",
		"ServiceUUIDs": []string{proto.ServiceUUID},
		"LocalName":    b.name,
		"Discoverable": true,
	})
	b.app = &application{objects: []*propObject{svc, info, request, b.response}}

	exports := []struct {
		v     any
		path  dbus.ObjectPath
		iface string
	}{
		{b.app, appPath, ifaceObjMgr},
		{svc, servicePath, ifaceProps},
		{info, infoPath, ifaceProps},
		{request, requestPath, ifaceProps},
		{b.response, responsePath, ifaceProps},
		{infoChar{p}, infoPath, ifaceGattChar},
		{requestChar{p}, requestPath, ifaceGattChar},
		{responseChar{p}, responsePath, ifaceGattChar},
		{adv, advPath, ifaceProps},
		{advertisement{b}, advPath, ifaceAdv},
		{agent{b.log}, agentPath, ifaceAgent},
	}
	for _, e := range exports {
		if err := b.conn.Export(e.v, e.path, e.iface); err != nil {
			return fmt.Errorf("exporting %s on %s: %w", e.iface, e.path, err)
		}
	}
	return nil
}

// propObject implements org.freedesktop.DBus.Properties for one interface.
type propObject struct {
	conn  *dbus.Conn
	path  dbus.ObjectPath
	iface string

	mu    sync.Mutex
	props map[string]dbus.Variant
}

func newPropObject(conn *dbus.Conn, path dbus.ObjectPath, iface string, props map[string]any) *propObject {
	o := &propObject{conn: conn, path: path, iface: iface, props: map[string]dbus.Variant{}}
	for k, v := range props {
		o.props[k] = dbus.MakeVariant(v)
	}
	return o
}

func (o *propObject) all() map[string]dbus.Variant {
	o.mu.Lock()
	defer o.mu.Unlock()
	return maps.Clone(o.props)
}

// set changes a property and emits PropertiesChanged.
func (o *propObject) set(iface, name string, v any) error {
	val := dbus.MakeVariant(v)
	o.mu.Lock()
	o.props[name] = val
	o.mu.Unlock()
	return o.conn.Emit(o.path, ifaceProps+".PropertiesChanged", iface, map[string]dbus.Variant{name: val}, []string{})
}

func (o *propObject) Get(iface, name string) (dbus.Variant, *dbus.Error) {
	if iface != o.iface {
		return dbus.Variant{}, dbus.NewError("org.freedesktop.DBus.Error.UnknownInterface", []any{iface})
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	v, ok := o.props[name]
	if !ok {
		return dbus.Variant{}, dbus.NewError("org.freedesktop.DBus.Error.UnknownProperty", []any{name})
	}
	return v, nil
}

func (o *propObject) GetAll(iface string) (map[string]dbus.Variant, *dbus.Error) {
	if iface != o.iface {
		return map[string]dbus.Variant{}, nil
	}
	return o.all(), nil
}

func (o *propObject) Set(iface, name string, v dbus.Variant) *dbus.Error {
	return dbus.NewError("org.freedesktop.DBus.Error.PropertyReadOnly", []any{name})
}

// application is the ObjectManager BlueZ reads the GATT service from.
type application struct {
	objects []*propObject
}

func (a *application) GetManagedObjects() (map[dbus.ObjectPath]map[string]map[string]dbus.Variant, *dbus.Error) {
	m := map[dbus.ObjectPath]map[string]map[string]dbus.Variant{}
	for _, o := range a.objects {
		m[o.path] = map[string]map[string]dbus.Variant{o.iface: o.all()}
	}
	return m, nil
}

// Options BlueZ passes to ReadValue and WriteValue.
func optDevice(opts map[string]dbus.Variant) dbus.ObjectPath {
	d, _ := opts["device"].Value().(dbus.ObjectPath)
	return d
}

func optUint16(opts map[string]dbus.Variant, name string) int {
	v, _ := opts[name].Value().(uint16)
	return int(v)
}

type infoChar struct{ p *peripheral }

func (c infoChar) ReadValue(opts map[string]dbus.Variant) ([]byte, *dbus.Error) {
	return c.p.readInfo(optDevice(opts), optUint16(opts, "mtu"), optUint16(opts, "offset"))
}

type requestChar struct{ p *peripheral }

func (c requestChar) WriteValue(value []byte, opts map[string]dbus.Variant) *dbus.Error {
	return c.p.writeRequest(optDevice(opts), optUint16(opts, "mtu"), optUint16(opts, "offset"), value)
}

type responseChar struct{ p *peripheral }

func (c responseChar) StartNotify() *dbus.Error { c.p.setNotifying(true); return nil }
func (c responseChar) StopNotify() *dbus.Error  { c.p.setNotifying(false); return nil }

// Confirm is called by BlueZ when the client confirms an indication.
func (c responseChar) Confirm() *dbus.Error { c.p.confirmed(); return nil }

type advertisement struct{ b *bluez }

// Release is called when BlueZ drops the advertisement on its own.
func (a advertisement) Release() *dbus.Error {
	a.b.advMu.Lock()
	a.b.advertised = false
	a.b.advMu.Unlock()
	a.b.log.Warn("BlueZ released the advertisement")
	return nil
}

// agent accepts Just Works pairing. The device has no screen or keyboard, so
// requests for a PIN or passkey are refused.
type agent struct{ log *slog.Logger }

func (a agent) Release() *dbus.Error { return nil }
func (a agent) Cancel() *dbus.Error  { return nil }

func (a agent) RequestPinCode(dev dbus.ObjectPath) (string, *dbus.Error) {
	return "", bluezError(errRejected, "no PIN entry on this device")
}

func (a agent) DisplayPinCode(dev dbus.ObjectPath, pin string) *dbus.Error {
	return bluezError(errRejected, "no display on this device")
}

func (a agent) RequestPasskey(dev dbus.ObjectPath) (uint32, *dbus.Error) {
	return 0, bluezError(errRejected, "no passkey entry on this device")
}

func (a agent) DisplayPasskey(dev dbus.ObjectPath, passkey uint32, entered uint16) *dbus.Error {
	return nil
}

func (a agent) RequestConfirmation(dev dbus.ObjectPath, passkey uint32) *dbus.Error {
	a.log.Info("pairing", "device", dev)
	return nil
}

func (a agent) RequestAuthorization(dev dbus.ObjectPath) *dbus.Error {
	a.log.Info("pairing", "device", dev)
	return nil
}

func (a agent) AuthorizeService(dev dbus.ObjectPath, uuid string) *dbus.Error {
	return nil
}
