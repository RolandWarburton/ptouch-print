# ptouch-print

Prints labels on a Brother PT-P700 P-touch printer from anywhere.

**`ptouch-print-board`** (client) and **`ptouch-command-server`** (control server)
work as a pull model, the client requests updates intermittently
allowing for one-way communication.

It supports 12 mm and 24 mm tape. Text, Barcodes, and PNGs.

```
  claude.ai                scripts, curl
  (signs in via Dex)       (client token)
      │ MCP /mcp                │ REST /api
      └───────────┬─────────────┘
                  ▼
  ┌──────────────────────────────────────┐
  │ Docker host, behind Traefik (HTTPS)  │
  │                                      │
  │   ptouch-command-server              │
  │     REST + MCP ──▶ job service       │
  │                      │               │
  │                      ▼               │
  │                 SQLite (jobs volume) │
  └──────────────────────────────────────┘
                  ▲
                  │ outgoing HTTPS only (SBC token):
                  │   GET  /board/next              every 15 s
                  │   POST /board/jobs/{id}/result
                  │
  ┌──────────────────────────────────────┐
  │ SBC                                  │
  │                                      │
  │   ptouch-print-board                 │
  │     draws the barcode, prints it     │
  └──────────────────┬───────────────────┘
                     │ USB (/dev/usb/lp0)
                     ▼
               Brother PT-P700

  queued ──▶ sent ──▶ done | failed     (never retried)
```

**Labels are never reprinted automatically.** A job that fails, or might have
printed but wasn't confirmed, is abandoned; submit a new one if the label really
didn't come out. Submits are refused straight away if the SBC hasn't checked in
for a minute, or if it reports the printer can't print. The refusal names the
problem and how to fix it, e.g. `editor_lite`: "Hold its Editor Lite button
until the light goes off."

## Setup and use

The wiki covers the rest:

- [Deploy the command server](https://github.com/RolandWarburton/ptouch-print/wiki/Deploy-the-command-server),
  including the claude.ai connector
- [Set up the SBC](https://github.com/RolandWarburton/ptouch-print/wiki/Set-up-the-SBC): imaging the
  Pi, installing and upgrading, and checking it can see the printer
- [Tokens](https://github.com/RolandWarburton/ptouch-print/wiki/Tokens): the SBC token, named client
  tokens and Dex sign-in, and how to make, revoke and replace them
- [Using the API](https://github.com/RolandWarburton/ptouch-print/wiki/Using-the-API): job states, PNG
  labels and the MCP tools

To print from Claude with the connector added: "print a barcode label for
TG17573". With a client token:

```sh
curl -H "Authorization: Bearer $TOKEN" -d '{"kind":"barcode","text":"TG17573"}' \
  https://ptouch.example.com/api/jobs
```

## Build

```sh
make test     # vet and unit tests
make build    # both binaries for this machine, in bin/
make armv6    # bin/ptouch-print-board-linux-armv6 (Pi Zero / Zero W, Pi 1)
make armv7    # bin/ptouch-print-board-linux-armv7
make arm64    # bin/ptouch-print-board-linux-arm64
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
