//go:build !linux

package board

// WiFiName is the Wi-Fi network's name; only the Linux board can read it.
func WiFiName() string { return "" }
