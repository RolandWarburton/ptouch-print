# ptouch-print-server

Bluetooth Low Energy (BLE) print server for a Brother PT-P700 P-touch label
printer. It runs on a Raspberry Pi Zero W with the printer plugged in over USB,
and clients print from a browser over Web Bluetooth. Nothing listens on the
network; Wi-Fi is only for SSH.

It supports 12 mm and 24 mm tape.

## Deploy

From this repo, for a Pi Zero W:

```sh
make armv6
scp bin/ptouch-print-server-linux-armv6 <pi>:ptouch-print-server
scp deploy/* <pi>:
ssh <pi> sudo sh setup-pi.sh
```

The server generates its API key on first start and `setup-pi.sh` prints it at
the end. Paste it into the web client. To see it again:

```sh
ssh <pi> sudo cat /var/lib/ptouch-print-server/api-key
```

To change the key, delete that file and restart the service. The server
generates a new one on start, and every client then needs it:

```sh
ssh <pi> 'sudo rm /var/lib/ptouch-print-server/api-key && sudo systemctl restart ptouch-print-server'
ssh <pi> sudo cat /var/lib/ptouch-print-server/api-key
```

Don't write your own key into that file as root: the service runs as the
`ptouch` user and the file must be owned by it, mode 0600. Let the server
generate it.

`setup-pi.sh` is also how you upgrade: copy over the new binary and run it
again. It:

- Unblocks Bluetooth in rfkill and sets `/etc/bluetooth/main.conf` to LE only,
  `JustWorksRepairing = always` and `[GATT] Client = false`.
- Creates the `ptouch` system user, installs the binary to
  `/usr/local/bin/ptouch-print-server`, and installs
  `/etc/ptouch-print-server/config.toml` if there isn't one yet (see
  [deploy/config.example.toml](deploy/config.example.toml)).
- Installs the udev rule and the systemd unit, restarts the service, and prints
  the API key.

On the Pi:

- Don't install CUPS. Its USB backend can take the printer away from the
  kernel's `usblp` driver, and then the server can't open it.
- Turn off Editor Lite mode on the printer. In that mode the printer shows up as a
  USB drive, and the server reports `editor_lite`.
- Check it can see the printer, and watch the log:

  ```sh
  lsusb | grep 04f9:2061
  journalctl -u ptouch-print-server -f
  ```

## Web client

`web/` is a static page that talks to the Pi over Web Bluetooth. It shows the
loaded tape and printer errors, and prints barcodes, text labels and PNGs.

```sh
cd web
deno task serve    # builds and serves on http://localhost:8000
deno task build    # just builds dist/
deno task test     # framing and HMAC tests
```

To host it somewhere else, copy `web/dist/` to any HTTPS static host. Web
Bluetooth only works in a secure context: HTTPS or `localhost`.

- **Browsers:** Chrome or Edge on desktop or Android. Not Firefox, Safari or
  anything on iOS. Chromium on Linux may need
  `chrome://flags/#enable-experimental-web-platform-features`.
- **Range:** the device running the browser has to be within Bluetooth range of
  the Pi. Where the page is hosted doesn't matter.
- **Connecting:** enter the API key, click Connect and pick the printer from the
  browser's list. "Remember the key in this browser" keeps it in localStorage.
- **Pairing:** the first connection pairs through the OS (Just Works, no PIN).
  The OS needs a Bluetooth agent running; GNOME and KDE have one, bare window
  managers such as Sway don't.
- **One client at a time:** the Pi accepts a single connection, and disconnects
  it after `idle_timeout` (2 minutes) with no requests.

## Bluetooth API

One GATT service, `c30d0001-ccea-4d82-aa2f-1ed9328fb057`, with an encrypted
`info` characteristic, a `request` characteristic for writes and a `response`
characteristic for indications. Requests and responses are chunked to fit the
MTU. Responses are JSON.

| Op | What it does |
| --- | --- |
| `auth` | HMAC-SHA256 of the API key over a per-connection nonce. The key is never sent. |
| `status` | Returns the loaded tape, the image height to use, and any printer errors. |
| `barcode` | Prints a Code 128 label: `{"text": "...", "tape_mm": optional}`. |
| `image` | Prints a PNG rendered at exactly the height `status` gave. |

The API key is checked at the application level because the Pi Zero W's
Bluetooth 4.1 only supports LE Legacy pairing, whose link encryption can be
broken.

An `unconfirmed` error with `"may_have_printed": true` means the label was sent
but the printer never confirmed it printed. Check the printer before retrying,
or you may get a duplicate label.

## Build

```sh
make test     # vet and unit tests
make armv6    # bin/ptouch-print-server-linux-armv6 (Pi Zero / Zero W, Pi 1)
make armv7    # bin/ptouch-print-server-linux-armv7
make arm64    # bin/ptouch-print-server-linux-arm64
```

The binary is pure Go (BlueZ over D-Bus, no cgo), so it cross-compiles for every
board. It needs BlueZ on the board.

## Debugging

- `journalctl -u ptouch-print-server -f` for the server.
- `sudo btmon` on the Pi or the client for the Bluetooth traffic.
- If `bluetoothd` restarts, the server exits and systemd restarts it.

## License

Copyright (C) 2026 Roland Warburton.

This program is free software: you can redistribute it and/or modify it under
the terms of the GNU General Public License as published by the Free Software
Foundation, either version 3 of the License, or (at your option) any later
version. It is distributed in the hope that it will be useful, but WITHOUT ANY
WARRANTY. See [LICENSE](LICENSE) for the full text.
