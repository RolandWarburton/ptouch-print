# Printing to a DYMO LabelWriter 450 / 550 from an ESP32-S3

Findings from research. Byte-level details come from the thermal-label
transcription of DYMO's technical references and the LPrint Dymo driver
(`lprint-dymo.c`). The "Verified" sections were checked on real printers; items
marked **verify** still must be.

## Transport

Both families are full-speed USB printer-class devices (VID `0x0922`,
interface class `0x07`) with one bulk OUT endpoint (commands and raster) and
one bulk IN endpoint (status). Endpoint addresses are not documented, so read
them from the configuration descriptor. PIDs listed by thermal-label: 450
`0x0020`, 450 Turbo `0x0021`, 550 `0x0028` (**verify**), 550 Turbo `0x0029`.

The ESP32-S3 has a USB OTG full-speed host (GPIO19 = D-, GPIO20 = D+). ESP-IDF's
USB Host library has no printer-class driver, so write a small custom client:
`usb_host_install` → daemon task → `usb_host_client_register` → on `NEW_DEV`
`usb_host_device_open` → `usb_host_interface_claim` → `usb_host_transfer_submit`
on the bulk endpoints. Transfers are async only and `timeout_ms` is not
implemented, so timeouts are ours to handle.

## Verified on a LabelWriter 450

- VID `0x0922`, PID `0x0020`, one printer-class interface (class 7, subclass 1,
  protocol 2), bulk OUT `0x02`, bulk IN `0x82`, 64-byte packets.
- `ESC A` returns one byte, `0x03` when idle.
- `ESC V` returns 10 ASCII bytes: `1750111d0A` (model `1750111`, byte 7 `d`,
  firmware `0A`).
- A test label printed from the ESP32 with the stream below. x runs across the
  head (dot 0 is at the label's edge, so a 36 mm label covers about dots
  0-410), y runs along the feed direction, and text drawn along x reads
  upright with the label's long edge vertical.
- Stock in use: DYMO Large Address Labels S0722400, 36 x 89 mm, which is 420
  dots wide by 1050 rows long at 300 dpi.
- The board's USB-OTG pad must be bridged so the USB port carries 5 V;
  without it the printer is never detected.

## Verified on a LabelWriter 550 Turbo

Checked 2026-11-25 with S0722400 labels: two portrait text labels and one
landscape barcode, each its own job. The barcode, `ESP32 / Dymo 550`, can't have been
right: the firmware's Code 128 pattern for a space was wrong then. The server draws
barcodes now.

- VID `0x0922`, PID `0x0029`, one printer-class interface (class 7, subclass 1,
  protocol 2), bulk OUT `0x02`, bulk IN `0x82`, 64-byte packets.
- `ESC V` returns 34 bytes starting `LW550T_PPB_00002`, `FWAP`, `0002`, `0025`.
- The lock can be refused at first: in two runs the first request was refused,
  and in one of those a request 3 s later was granted. Keep asking.
- State `5` is also the normal state when this host holds no lock.
- The SKU and the `ESC U` label info read as zeros until a lock has been
  granted. The SKU still read after the release, but as zeros again after the
  board was reset.
- `ESC U` returns 64 bytes. Lengths are in tenths of a mm: label 88.6 x 35.7,
  pitch 93.1 (about 1100 dots; `label.c`'s `pitch` of 1050 is what the 450 uses).
- `ESC D`'s `w` is 950 rows and `h` 400 dots for this stock, so 50 bytes a row
  rather than 84. Rows go in feed order, bit 7 of the first byte is dot 0, and
  `label_head_row` needs no change.
- The status reply after `ESC G` takes about 1.6 s and says state `1`
  (printing), with the remaining count already one lower.
- Not tried: several labels in a job, running out, a jam, an open bay, other
  stock, or the plain 550.

## Verified on a Brother PT-P700

Checked 2026-11-25 with 24 mm laminated tape: 100 status requests and one 31 mm label.

- VID `0x04f9`, PID `0x2061`, one printer-class interface, bulk OUT `0x02`, bulk IN
  `0x81`, 64-byte packets. Self-powered (asks for 0 mA).
- With nothing to say it answers a bulk IN with an **empty packet**, so reads don't hang
  and must be polled. 154 reads canceled at once (a timeout of 0) did no harm: the
  endpoint worked on.
- Its port was cut in software and restored 25 times, and it was found again each time. Cut
  1 s into a print, the label still came out, so `unconfirmed` may well have printed; cut
  after 20 ms, none did. While it finishes such a label it doesn't answer (`no_reply`).
- Status comes within 2 ms as one 32-byte packet: `80 20 42 30 67 30 00 00 00 00 18 01`,
  then zeros but for bytes 24 and 25 (`01 08`). Byte 18 is `0` in a reply.
- A job is followed by messages the printer sends by itself: type `6` at once, then type
  `1` (completed) about 6 s later, cutting included.
- The job sent in 3200-byte chunks prints as Go's single write did. The picture's top is
  pin 0, and its first row is the label's left end.
- Text, a barcode and a PNG drawn by the server printed right.
- Not tried: 12 mm tape, long labels, errors, Editor Lite, the printer's sleep, replugging
  by hand.

## LabelWriter 450 (start here)

300 dpi, 672-dot head, 84 bytes per raster row, bit 7 = leftmost dot.

```
1B x100            resync (LPrint sends 100 ESC bytes first)
1B 40              ESC @   reset
1B 42 00           ESC B   dot tab 0
1B 44 54           ESC D   bytes per line = 84
1B 65              ESC e   normal density (c/d/e/g = light..dark)
1B 68              ESC h   300x300 (1B 69 = 300x600 graphics mode)
1B 4C hi lo        ESC L   label length in 1/300 in, big-endian
repeat per row:    16 <84 bytes>        SYN + one raster row
                   1B 66 01 n           skip n blank rows (optional)
1B 45              ESC E   form feed to tear bar
1B 41              ESC A   status request -> 1 byte on bulk IN
```

Status byte: `0x01` ready, `0x02` top of form, `0x20` out of labels,
`0x40` jam, `0x80` error. Healthy idle is `0x03`.

Two independent sources agree on this stream (SYN rows, ESC D = bytes per
line, ESC L in 1/300 in). LPrint never reads status, so the status byte is
documented by thermal-label only (**verify**).

## LabelWriter 550

Same head, but a different, stateful protocol with a lock handshake:

```
1B 73 <jobId u32 LE>          ESC s   start job (mandatory)
1B 41 01  -> read 32 bytes    ESC A   acquire lock; status byte 0 must be 0..3 (5 = denied)
1B 6E 00 00                   ESC n   label index
1B 44 01 02 <w u32> <h u32>   ESC D   bpp=1, align=2, w = rows in feed direction, h = dots across head
<w * ceil(h / 8) raw bytes>   no per-row framing
1B 47                         ESC G   feed to print head
1B 41 00  -> read 32 bytes    ESC A   status, mandatory before ESC E
1B 45                         ESC E   feed to tear
1B 51                         ESC Q   end job, releases lock
```

Pitfalls: streaming a whole job without the status reads hangs the printer
until power-cycled.
The 32-byte status has the label SKU, remaining count and a media byte (offset
10; `10` = counterfeit). **The 550 only prints genuine DYMO labels (NFC tag
checked when the cassette is inserted).** LPrint has no 550 support.

## Hardware (unconfirmed, check the board's docs)

- The DevKitC-1 style board has two USB-C ports: one to a UART bridge, one
  native USB (GPIO19/20). Host mode uses the native one.
- The ESP32-S3 does not generate VBUS. The printer needs 5 V on VBUS to see a
  host; it is self-powered otherwise. Some boards need a jumper/pad set to
  route 5 V to the native port's VBUS.
- Cable: the printer has a USB-B socket. ESP32 native USB-C → printer needs a
  USB-C (OTG) to USB-A female adapter plus the printer's own A-to-B cable, or a
  USB-C to USB-B cable. A plain A-to-C cable is a device-side cable.
- N16R8 = 16 MB flash, 8 MB octal PSRAM: GPIO35-37 (and 26-32) are not usable.

## Plan

1. Bring-up firmware: host stack, print VID/PID and descriptors (endpoint
   addresses) of whatever is plugged in.
2. 450: send ESC V / ESC A, then a test bitmap using the stream above.
3. 550: implement the lock handshake after the 450 works. (Done: `apps/lw550`.)
