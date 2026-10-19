#!/bin/sh
# One-time setup of the SBC, and re-run to upgrade. Run from a directory holding
# ptouch-print-server (the binary for this board) and the deploy/ files:
#   sudo sh setup-pi.sh
# Wi-Fi is set when imaging (Raspberry Pi Imager); nothing here touches it.
# The board's token (from `ptouch-command-server token`) goes in
# /etc/ptouch-print-server/token; this script creates the file if it's missing.
set -eu
cd "$(dirname "$0")"

# Service user, binary and config.
id ptouch >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin ptouch
install -m 755 ptouch-print-server /usr/local/bin/ptouch-print-server
install -d /etc/ptouch-print-server
[ -f /etc/ptouch-print-server/config.toml ] || install -m 644 config.example.toml /etc/ptouch-print-server/config.toml
[ -f /etc/ptouch-print-server/token ] || install -m 600 -o ptouch -g ptouch /dev/null /etc/ptouch-print-server/token

install -m 644 99-ptouch.rules /etc/udev/rules.d/
udevadm control --reload
udevadm trigger --subsystem-match=usbmisc

# Left over from the Bluetooth version.
rm -rf /var/lib/ptouch-print-server

install -m 644 ptouch-print-server.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable ptouch-print-server
if [ -s /etc/ptouch-print-server/token ] && ! grep -q 'ptouch.example.com' /etc/ptouch-print-server/config.toml; then
	systemctl restart ptouch-print-server
	sleep 3
	systemctl --no-pager status ptouch-print-server | head -15
else
	echo "Not started yet. Set url in /etc/ptouch-print-server/config.toml and put the board token in"
	echo "/etc/ptouch-print-server/token, then: systemctl restart ptouch-print-server"
fi
