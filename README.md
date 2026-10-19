# ptouch-print-server

Prints labels on a Brother PT-P700 P-touch printer from anywhere. Two binaries:

- **`ptouch-print-server`** runs on a Raspberry Pi Zero W with the printer on USB.
  Every 15 s it asks the command server for a label, prints it and reports back.
  It listens on nothing; all of its traffic is outgoing HTTPS.
- **`ptouch-command-server`** runs in Docker behind Traefik. It queues labels
  submitted over REST or MCP (so you can ask Claude to "print a label for
  TG17573") and hands them to the board.

It supports 12 mm and 24 mm tape. Labels are Code 128 barcodes drawn on the board
for whatever tape is loaded, or PNGs rendered for a specific tape.

**Labels are never reprinted automatically.** A job that fails, or might have
printed but wasn't confirmed, is abandoned; submit a new one if the label really
didn't come out. Submits are refused straight away if the board hasn't checked in
for a minute, or if it reports the printer can't print. The refusal names the
problem and how to fix it, e.g. `editor_lite`: "Hold its Editor Lite button
until the light goes off."

The protocol is in [docs/ptouch-server-spec.md](docs/ptouch-server-spec.md) §8.

## Tokens

There are three kinds of credential. The server stores only SHA-256 hashes of
the static tokens, so a token is shown once, when you make it.

| Who | Credential | Server setting | Where the token goes |
| --- | --- | --- | --- |
| The board | board token | `PTOUCH_BOARD_TOKEN_SHA256` | `/etc/ptouch-print-server/token` on the board, owner `ptouch`, mode `0600` |
| Scripts and other systems | named client token | `PTOUCH_CLIENT_TOKENS` (`name:hash,...`) | the client, as `Authorization: Bearer <token>` |
| People via claude.ai | Dex sign-in | `MCP_RESOURCE_URL`, `MCP_ALLOWED_EMAILS` | nowhere: claude.ai signs in through Dex |

Make a token with the server binary, locally (`make build`, then
`bin/ptouch-command-server token`) or on the server
(`docker compose run --rm ptouch ptouch-command-server token`):

```sh
ptouch-command-server token                 # the board's token
ptouch-command-server token -client ci      # a client token named "ci"
```

Each prints the token and the line to add to the server's `.env`. Then restart
the server (`docker compose up -d`).

- **Revoking a client:** remove its `name:hash` from `PTOUCH_CLIENT_TOKENS` and
  restart. Other clients keep working.
- **Replacing the board token:** make a new one, update
  `PTOUCH_BOARD_TOKEN_SHA256` and restart the server, then write the token on the
  board and restart it:

  ```sh
  ssh -t <pi> "echo '<token>' | sudo tee /etc/ptouch-print-server/token >/dev/null \
    && sudo systemctl restart ptouch-print-server"
  ```

  `tee` keeps the file's owner (`ptouch`) and mode (`0600`), which `setup-pi.sh`
  set when it created it. Check with `sudo ls -l /etc/ptouch-print-server/token`.

Client tokens work on `/api` and `/mcp`. The board token works only on `/board`.

## Deploy the command server

On the host that runs the other MCP servers (Docker, the external `services`
network, Traefik with a `letsencrypt` resolver):

1. Copy this repo to `~/services/ptouch/`.
2. `cp .env.example .env` and fill it in: the board token hash, any client
   tokens, and for claude.ai the Dex settings below.
3. `docker compose up -d --build`. It serves `https://ptouch.<MCP_DOMAIN>`.

Jobs live in SQLite on the `jobs` volume, kept for 365 days.

**claude.ai (MCP):** register a Dex client `ptouch-mcp`, set up like
bookstack-mcp's. Set `MCP_OIDC_ISSUER` to Dex's URL,
`MCP_RESOURCE_URL=https://ptouch.<MCP_DOMAIN>/mcp` and `MCP_ALLOWED_EMAILS` to
who may print. `MCP_OIDC_AUDIENCE` (default `ptouch-mcp`) rarely needs
changing. Then add `https://ptouch.<MCP_DOMAIN>/mcp` as a connector in
claude.ai. Without Dex, MCP clients can use a client token.

## Set up the board

1. **Image the SD card** with Raspberry Pi Imager (Raspberry Pi OS Lite, 32-bit).
   In its settings, set the hostname, your SSH key, and the Wi-Fi SSID and
   password (the guest network is fine: the board only needs outgoing HTTPS).
2. **Plug the printer into the data micro-USB port** (not PWR), and turn off the
   printer's Editor Lite mode.
3. **Install** from this repo:

   ```sh
   make armv6
   ssh <pi> 'mkdir -p ~/ptouch'
   scp bin/ptouch-print-server-linux-armv6 <pi>:ptouch/ptouch-print-server
   scp deploy/* <pi>:ptouch/
   ssh -t <pi> 'cd ~/ptouch && sudo sh setup-pi.sh'
   ```

4. **Configure:** set `url` in `/etc/ptouch-print-server/config.toml` (see
   [deploy/config.example.toml](deploy/config.example.toml)), and write the board
   token into `/etc/ptouch-print-server/token` as shown under
   [Tokens](#tokens). Then `sudo systemctl restart ptouch-print-server`.

`setup-pi.sh` is also how you upgrade: copy over the new binary and run it again.
It creates the `ptouch` user, installs the binary, the config (if missing), an
empty token file (owner `ptouch`, mode `0600`, if missing), the udev rule and the
systemd unit. It starts the service only once the URL and token are set.

On the Pi:

- Don't install CUPS. Its USB backend can take the printer away from the
  kernel's `usblp` driver.
- Check it can see the printer and the server:

  ```sh
  lsusb | grep 04f9:2061
  journalctl -u ptouch-print-server -f
  ```

  Then `GET /api/status` on the server should say `"online": true` with the
  loaded tape. If the printer can't print, it shows `"problem"` instead, such as
  `editor_lite` or `printer_error` (cover open, no tape).

## Print

From Claude, with the connector added: "print a barcode label for TG17573". The
tools are `print_barcode`, `get_job`, `list_jobs` and `printer_status`.

With a client token:

```sh
curl -H "Authorization: Bearer $TOKEN" -d '{"kind":"barcode","text":"TG17573"}' \
  https://ptouch.example.com/api/jobs
curl -H "Authorization: Bearer $TOKEN" https://ptouch.example.com/api/jobs/1
```

A job goes `queued`, then `sent` when the board takes it (within 15 s), then
`done` or `failed`. A job that stays `sent` was taken but never reported: check
the printer before printing it again. For a PNG, get the height from
`GET /api/status` (`height_dots`) and send
`{"kind":"image","tape_mm":24,"image":"<base64 PNG>"}`.

## Build

```sh
make test     # vet and unit tests
make build    # both binaries for this machine, in bin/
make armv6    # bin/ptouch-print-server-linux-armv6 (Pi Zero / Zero W, Pi 1)
make armv7    # bin/ptouch-print-server-linux-armv7
make arm64    # bin/ptouch-print-server-linux-arm64
```

Both binaries are pure Go (no cgo). The command server's image is built by the
`Dockerfile`.

## License

Copyright (C) 2026 Roland Warburton.

This program is free software: you can redistribute it and/or modify it under
the terms of the GNU General Public License as published by the Free Software
Foundation, either version 3 of the License, or (at your option) any later
version. It is distributed in the hope that it will be useful, but WITHOUT ANY
WARRANTY. See [LICENSE](LICENSE) for the full text.
