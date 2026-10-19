#!/bin/sh
# One-time setup of the SBC, and re-run to upgrade. Run from a directory holding
# ptouch-print-board (the binary for this board) and the deploy/board/ files:
#   sudo sh setup-pi.sh
# Wi-Fi is set when imaging (Raspberry Pi Imager); nothing here touches it.
# The board's token (from `ptouch-command-server token`) goes in
# /etc/ptouch-print-board/token; this script creates the file if it's missing.
set -eu
cd "$(dirname "$0")"

# Service user, binary and config.
id ptouch >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin ptouch
install -m 755 ptouch-print-board /usr/local/bin/ptouch-print-board
install -d /etc/ptouch-print-board
[ -f /etc/ptouch-print-board/config.toml ] || install -m 644 config.example.toml /etc/ptouch-print-board/config.toml
[ -f /etc/ptouch-print-board/token ] || install -m 600 -o ptouch -g ptouch /dev/null /etc/ptouch-print-board/token

install -m 644 99-ptouch.rules /etc/udev/rules.d/
udevadm control --reload
udevadm trigger --subsystem-match=usbmisc

install -m 644 ptouch-print-board.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable ptouch-print-board
if [ -s /etc/ptouch-print-board/token ] && ! grep -q 'ptouch.example.com' /etc/ptouch-print-board/config.toml; then
	systemctl restart ptouch-print-board
	sleep 3
	systemctl --no-pager status ptouch-print-board | head -15
else
	echo "Not started yet. Set url in /etc/ptouch-print-board/config.toml and put the board token in"
	echo "/etc/ptouch-print-board/token, then: systemctl restart ptouch-print-board"
fi
