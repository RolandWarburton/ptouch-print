# ptouch-print

Prints labels on label printers from anywhere: a Brother PT-P700 P-touch and DYMO
LabelWriters.

**`ptouch-command-server`** queues labels. Each printer has an ESP32 **board**
([`firmware/`](firmware/README.md)) that asks the server for its labels, so all of a
board's traffic is outgoing.

> The boards' firmware doesn't talk to the server yet. The Raspberry Pi board it
> replaces is last in commit `6b5b34f`.

Text, barcodes and PNGs, on 12 mm and 24 mm tape and on LabelWriter labels. A job names
its printer when the server has more than one.

```
  MCP client               scripts, curl
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
                  │ outgoing HTTPS only (board token):
                  │   GET  /board/next              every 15 s
                  │   POST /board/jobs/{id}/result
                  │
  ┌──────────────────────────────────────┐
  │ ESP32-S3 board                       │
  │                                      │
  │   firmware for its type of printer   │
  │     prints the label                 │
  └──────────────────┬───────────────────┘
                     │ USB
                     ▼
               label printer

  queued ──▶ sent ──▶ done | failed     (never retried)
```

**Labels are never reprinted automatically.** A job that fails, or might have
printed but wasn't confirmed, is abandoned; submit a new one if the label really
didn't come out. Submits are refused straight away if the board hasn't checked in
for a minute, or if it reports the printer can't print. The refusal names the
problem and how to fix it, e.g. `editor_lite`: "Hold its Editor Lite button
until the light goes off."

## Setup and use

The wiki covers the rest:

- [Deploy the command server](https://github.com/RolandWarburton/ptouch-print/wiki/Deploy-the-command-server),
  including the claude.ai connector
- [Tokens](https://github.com/RolandWarburton/ptouch-print/wiki/Tokens): the board token, named client
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
make build    # the command server for this machine, in bin/
```

The server is pure Go (no cgo), and its image is built by the `Dockerfile`. The
firmware has its own Makefile: see [`firmware/`](firmware/README.md).

## License

Copyright (C) 2026 Roland Warburton.

This program is free software: you can redistribute it and/or modify it under
the terms of the GNU General Public License as published by the Free Software
Foundation, either version 3 of the License, or (at your option) any later
version. It is distributed in the hope that it will be useful, but WITHOUT ANY
WARRANTY. See [LICENSE](LICENSE) for the full text.
