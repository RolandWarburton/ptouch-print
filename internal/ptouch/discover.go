package ptouch

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	vendorBrother           = "04f9"
	productPTP700           = "2061"
	productPTP700EditorLite = "2064" // mass-storage mode, no printer interface
)

var (
	ErrNotConnected = errors.New("printer not connected")
	ErrEditorLite   = errors.New("printer is in Editor Lite mode, turn Editor Lite off")
	ErrPermission   = errors.New("no permission to open the printer")
)

// Find returns the /dev/usb/lpN path of the PT-P700. sysRoot and devRoot are
// normally "/sys" and "/dev"; tests point them at a fake tree.
func Find(sysRoot, devRoot string) (string, error) {
	devs, _ := filepath.Glob(filepath.Join(sysRoot, "class", "usbmisc", "lp*"))
	for _, dev := range devs {
		// "device" links to the USB interface; its parent is the USB device.
		// Resolve the link first: filepath.Join would drop "device/.." as text.
		iface, err := filepath.EvalSymlinks(filepath.Join(dev, "device"))
		if err != nil {
			continue
		}
		if usbID(filepath.Dir(iface)) == vendorBrother+":"+productPTP700 {
			return filepath.Join(devRoot, "usb", filepath.Base(dev)), nil
		}
	}

	usbDevs, _ := filepath.Glob(filepath.Join(sysRoot, "bus", "usb", "devices", "*"))
	for _, dev := range usbDevs {
		if usbID(dev) == vendorBrother+":"+productPTP700EditorLite {
			return "", ErrEditorLite
		}
	}
	return "", ErrNotConnected
}

// usbID returns "vendor:product" for a USB device directory in sysfs.
func usbID(dir string) string {
	vendor, err1 := readTrimmed(filepath.Join(dir, "idVendor"))
	product, err2 := readTrimmed(filepath.Join(dir, "idProduct"))
	if err1 != nil || err2 != nil {
		return ""
	}
	return vendor + ":" + product
}

func readTrimmed(path string) (string, error) {
	b, err := os.ReadFile(path)
	return strings.TrimSpace(string(b)), err
}

// DefaultOpener finds the printer through the real sysfs and opens it.
func DefaultOpener() Opener {
	return func() (Device, error) { return openDevice("/sys", "/dev") }
}

func openDevice(sysRoot, devRoot string) (Device, error) {
	path, err := Find(sysRoot, devRoot)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	switch {
	case errors.Is(err, fs.ErrPermission):
		return nil, fmt.Errorf("%w %s: add the service user to the lp group", ErrPermission, path)
	case errors.Is(err, fs.ErrNotExist): // unplugged between Find and open
		return nil, ErrNotConnected
	case err != nil:
		return nil, err
	}
	return f, nil
}
