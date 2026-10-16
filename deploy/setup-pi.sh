#!/bin/sh
# One-time setup of the SBC, and re-run to upgrade. Run from a directory holding
# ptouch-print-server (the binary for this board) and the deploy/ files:
#   sudo sh setup-pi.sh
# The server generates its API key on first start; this prints it at the end.
set -eu
cd "$(dirname "$0")"

# Bluetooth: unblock the radio (Raspberry Pi OS ships it soft-blocked), use LE
# only, and let a client that forgot its bond pair again without a person here.
# The GATT client is off: bluetoothd would otherwise browse each client's own
# GATT services and ask it to pair for them.
for r in /sys/class/rfkill/rfkill*; do
	[ "$(cat "$r/type")" = bluetooth ] && echo 0 >"$r/soft"
done
conf=/etc/bluetooth/main.conf
set_bt() { # section key value
	# Only touches the key inside its own section: the same key name can appear
	# in several (BlueZ has a "Client" under [CSIS]). Adds it at the end of the
	# section when missing, or the section at the end of the file.
	tmp=$(mktemp)
	awk -v sec="[$1]" -v key="$2" -v val="$3" '
		function put() { print key " = " val; done = 1 }
		$0 == sec { seen = 1; insec = 1; print; next }
		/^\[/ { if (insec && !done) put(); insec = 0 }
		insec && !done && $0 ~ "^#? *" key " *=" { put(); next }
		{ print }
		END {
			if (insec && !done) put()
			if (!seen) { print ""; print sec; put() }
		}' "$conf" >"$tmp"
	cat "$tmp" >"$conf"
	rm -f "$tmp"
}
set_bt General ControllerMode le
set_bt General JustWorksRepairing always
set_bt GATT Client false
systemctl restart bluetooth

# Service user, binary and config.
id ptouch >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin ptouch
install -m 755 ptouch-print-server /usr/local/bin/ptouch-print-server
install -d /etc/ptouch-print-server
[ -f /etc/ptouch-print-server/config.toml ] || install -m 644 config.example.toml /etc/ptouch-print-server/config.toml

install -m 644 99-ptouch.rules /etc/udev/rules.d/
udevadm control --reload
udevadm trigger --subsystem-match=usbmisc

install -m 644 ptouch-print-server.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable ptouch-print-server
systemctl restart ptouch-print-server
sleep 3
systemctl --no-pager status ptouch-print-server | head -15
echo "API key: $(cat /var/lib/ptouch-print-server/api-key)"
