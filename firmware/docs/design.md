# Multiple printers and label types

One command server drives any number of printers: P-touch tape printers and
DYMO LabelWriters. Each printer has its own board that pulls jobs, as today.

## Printers

A printer is a named target the server knows about:

| field | meaning |
|---|---|
| `name` | what jobs address it by, e.g. `office-dymo` |
| `family` | `ptouch`, `lw450` or `lw550`; decides which label kinds and options apply |
| token | SHA-256 of the board's token; the token identifies the printer, so a board can only fetch and report its own jobs |

Configured as `PRINT_PRINTERS=name:family:sha256,...`.

The server keeps what each board's last poll said, per printer: when it
polled, the loaded media, why it can't print (if so) and its Wi-Fi network.
A printer is offline after 60 s without a poll; submits to it are refused with
the same reasons and fixes as now.

## Media

Media is the label stock loaded in a printer. A media definition is data:

| field | meaning |
|---|---|
| `id` | `tape-12`, `tape-24`, `large-address`, ... |
| `family` | which printers take it |
| `width`, `length` | printable dots across the head and along the feed; `length` is 0 for tape, which is cut to fit the label |
| `pitch` | a label's length from one to the next, in dots (die-cut stock) |

The same table lives in the Go `wire` package and in the firmware. The
loaded media comes from, in order of trust:

1. the printer, when it can say (P-touch reports its tape width; the LW550
   reports the label SKU from its NFC tag);
2. the board's configuration, for printers that can't (the LW450).

Orientation defaults to `portrait`. Media the server doesn't know is the problem
`unsupported_media`.

The board sends the media id on every poll. The server refuses jobs that
can't fit it, so a label never prints on the wrong stock.

## Jobs

`POST /api/jobs` takes the existing fields plus:

- `printer`: the target. May be left out when exactly one printer is
  configured; otherwise the refusal lists the names.
- `orientation` (DYMO text and barcode): `portrait` or `landscape`.
- `media` (optional): the media the job expects. A mismatch with what is
  loaded is refused as `wrong_media`.

Per family:

- P-touch: unchanged. One or two lines of text, or a barcode, or a PNG.
- LabelWriter: 1 to 8 lines of text, a barcode, or a PNG rendered for the media's
  width and length. Text that doesn't fit is refused with the size it needed
  and the size the label has.

Jobs are stored with their `printer`, `media` and `orientation`, and
`GET /board/next` returns only the asking printer's next job. The result codes
are shared; `wrong_media` is new. Labels are never retried, as now.

## Poll

`GET /board/next?media=<id>` with the board's token. When the printer can't
print, it sends `media=` empty and `problem=<code>`, and may add `wifi=<name>`.

## Clients

- REST: `printer` and `orientation` on submit; `GET /api/status` lists every
  printer with its family, online state, media and problem.
- MCP: the print tools take an optional `printer`; `printer_status` lists
  printers when none is named.
- Web UI: a printer picker when more than one is configured, and orientation
  for LabelWriter text and barcodes.

## The ESP32-S3 board

Modules:

- `label`: draws text and Code 128 on a canvas and produces the head's raster
  rows. Plain C, runs on a development machine.
- `media`: the media table and lookup by id.
- `dymo450` / `dymo550`: print a drawn label, check status. `dymo550_proto` is
  the 550's parsing, in plain C. The 450's code is still in `main.c`.
- `usb_io`: one bulk transfer with a timeout.
- `config`: Wi-Fi credentials, server URL, board token and media id, from
  NVS with build-time defaults.
- `wifi`, `poller`: join the network and run the poll loop over HTTPS.

Print flow for a job: check the job's media against the loaded media, draw the
label, check the printer is idle, stream it, read the status, report the
result. A job that fails or might have printed is not retried.

## Order of work

1. Board: media table and stock setting (done), then `config`, `wifi`.
2. Board: `poller` against a local copy of the server.
3. Server: printers, per-printer state, media-aware refusals, schema, `printer`
   on REST, MCP and the web UI. (Built on ptouch-print's `multiple-printers` branch;
   not deployed or tried with this board yet. `lw550` is refused until step 4.)
4. LW550 support: lock handshake, status and NFC media. (Board side done in
   `dymo550`; the server still refuses `lw550`.)
