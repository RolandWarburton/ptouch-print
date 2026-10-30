package board

import (
	"os"
	"strings"
	"syscall"
	"unsafe"
)

// siocgiwessid is the wireless-extensions ioctl that reads an interface's network name.
const siocgiwessid = 0x8B1B

// iwreq is the kernel's struct iwreq with its union read as an iw_point: 32
// bytes on 32-bit ARM, which is as much as the kernel reads.
type iwreq struct {
	name   [16]byte
	essid  unsafe.Pointer
	length uint16
	flags  uint16
	_      [8]byte
}

// WiFiName is the name (SSID) of the Wi-Fi network the board is connected to,
// or "" when it isn't on one or the name can't be read. It asks the kernel
// directly, so it doesn't depend on which network tools the image has.
func WiFiName() string {
	data, err := os.ReadFile("/proc/net/wireless")
	if err != nil {
		return ""
	}
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, 0)
	if err != nil {
		return ""
	}
	defer syscall.Close(fd)
	// /proc/net/wireless has two header lines, then "wlan0: ..." per wireless interface.
	lines := strings.Split(string(data), "\n")
	for _, line := range lines[min(2, len(lines)):] {
		iface, _, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok || len(iface) >= 16 {
			continue
		}
		var buf [33]byte // IW_ESSID_MAX_SIZE, plus a NUL
		req := iwreq{essid: unsafe.Pointer(&buf[0]), length: uint16(len(buf))}
		copy(req.name[:], iface)
		_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), siocgiwessid, uintptr(unsafe.Pointer(&req)))
		if errno != 0 {
			continue
		}
		if name := strings.TrimRight(string(buf[:min(int(req.length), 32)]), "\x00"); name != "" {
			return name
		}
	}
	return ""
}
