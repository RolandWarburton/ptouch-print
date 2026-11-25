# Set up a board

A board is an ESP32-S3 that is the USB host of one label printer. It joins Wi-Fi, asks the
command server for its labels every 15 s and prints them. All its traffic is outgoing HTTPS.

Each type of printer has its own firmware and family:

| Printer | Firmware (`APP`) | Family |
|---|---|---|
| Brother PT-P700 | `ptp700` | `pt-p700` |
| DYMO LabelWriter 450 | `lw450` | `lw450` |
| DYMO LabelWriter 550, 550 Turbo | `lw550` | `lw550` |

## You need

- An ESP32-S3 board with 16 MB flash and 8 MB PSRAM (N16R8) and two USB-C ports: one to a
  UART bridge, one native USB (GPIO19/20).
- **5 V on the native USB port.** Most boards need a pad or jumper bridged, often marked
  USB-OTG. Without it the printer is never found.
- A data cable from the **UART** port to a computer, to flash it.
- A USB-C (OTG) to USB-A adapter and the printer's own cable, from the **native** port to
  the printer.
- A 2.4 GHz Wi-Fi network with a WPA2 or WPA3 password. No sign-in pages or user names. It
  must let the board reach the server (HTTPS), DNS and `pool.ntp.org` (NTP).
- A Linux computer with Docker and `make`. ESP-IDF runs in a container, so the first build
  downloads a large image.

## 1. Make the printer's token

On the server, with a name of your choosing (lower-case letters, digits, dashes) and the
family from the table:

```sh
docker compose run --rm ptouch ptouch-command-server token -printer office-dymo -family lw550
```

It prints the token, once, and an entry for `PRINT_PRINTERS` in the server's `.env`. Add the
entry, separated by a comma from any others, and restart with `docker compose up -d`.

Each printer needs its own token. With more than one printer, every job has to name one.

> Replacing a board keeps the printer's name: replace its entry instead of adding one. Stop
> the old board first.

## 2. Flash the firmware

Connect the UART port to the computer. Then, in `firmware/`, with the port it appeared as:

```sh
make build APP=lw550
make flash APP=lw550 PORT=/dev/ttyACM0
```

## 3. Write its settings

Copy the example. `settings*.toml` is gitignored; never commit one.

```sh
cp settings.example.toml settings.office-dymo.toml
chmod 600 settings.office-dymo.toml
```

Fill it in:

| Key | Value |
|---|---|
| `server` | the server's URL, which must be `https://` |
| `token` | the token from step 1 |
| `media` | LabelWriter 450 only, which can't tell what's in it: `large-address`. Leave it out otherwise. |
| `[[wifi]]` | a network, with its `ssid` and `pass`. Add one for each, up to four, the one to prefer first. |

Remove what you don't need. Strings are in double quotes; a backslash or double quote in a
password is written `\\` or `\"`, or put the value in single quotes to write it as it is.
`make settings` refuses a file with a missing or oversized value.

```sh
make settings SETTINGS=settings.office-dymo.toml PORT=/dev/ttyACM0
```

That replaces all the board's settings. They survive flashing firmware. Afterwards delete
the file, or keep it somewhere safe: the board can't show you its settings.

## 4. Connect the printer and check

Plug the printer into the **native** port and switch it on. A PT-P700's Editor Lite light
must be off.

```sh
make monitor APP=lw550 PORT=/dev/ttyACM0     # Ctrl-] leaves
```

```
settings: server https://ptouch.example.com, 2 Wi-Fi networks
usb_printer: 0922:0029: printer interface 0 alt 0, bulk OUT 0x02, bulk IN 0x82
board: state: ready: large-address
net: joined Office, channel 6, -46 dBm
```

Press `t` there to print the built-in sample label, which doesn't need the server. Then look
at the server's `/api/status`: the printer should be `online`, with its `media` and `wifi`.

The board needs power but not the computer after this.

## If it doesn't work

| In the monitor | Means |
|---|---|
| `no settings` | do step 3 |
| `server must be an https:// URL`, `no token`, `no Wi-Fi networks` | fix that setting |
| `can't join X: ..., reason 201` | no such network in range, or it is 5 GHz only |
| `can't join X: ..., reason 15`, `202` or `204` | usually a wrong password |
| `no answer from the time server` | NTP is blocked; certificates can't be checked without the time |
| `the server answered 401` | wrong token, or its entry isn't in `PRINT_PRINTERS` |
| `no answer from the server` | wrong URL, the server is down, or the network blocks it |
| `0922:0029 isn't a printer this firmware drives` | wrong firmware for the printer |
| `not_connected` | no printer found: its power, cable, the native port, and that port's 5 V. If it was replugged, try once more (see printing-notes.md) |
| `editor_lite` | turn off the PT-P700's Editor Lite |
| `unsupported_media` | the labels aren't in the media table, or a 450's `media` setting is wrong |
| `unsupported_tape` | the tape isn't 12 or 24 mm |
| `wrong_media`, `wrong_height` | what's loaded changed after the label was drawn; submit it again |

On the server, a printer whose board hasn't asked for a minute is `printer_offline`.

## Wi-Fi

A board calls itself `label-<firmware>-<end of its MAC address>`, such as
`label-ptp700-d0d4`; the monitor shows it and the whole address. Nothing connects to it: the
name is for whoever runs the network.

Networks are tried in order, taking each one's strongest access point. On a later network
the board looks for an earlier one every minute and moves to it. If it can't reach the server
for 5 minutes it tries another network, and leaves that one alone for 10. `/api/status` has
the network it's on.

## Later

- **Change a setting:** edit the file and do step 3 again, with every setting in it.
- **New firmware:** step 2 again. There are no updates over the network.
- **New token:** steps 1 and 3.
- **Give a board away:** `esptool.py erase_flash` removes the Wi-Fi passwords and token, which
  are kept unencrypted. Remove its entry from `PRINT_PRINTERS` too.
