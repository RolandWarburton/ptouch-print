# Board firmware

Firmware for the ESP32-S3 (N16R8) boards, each the USB host of one label printer.
Every device type has its own firmware, built from shared components.

| App | Printers | State |
|---|---|---|
| `apps/lw450` | DYMO LabelWriter 450 | not tried since the server draws |
| `apps/lw550` | DYMO LabelWriter 550 (not tried), 550 Turbo | not tried since the server draws |
| `apps/ptp700` | Brother PT-P700 | prints the server's jobs |

The server draws the labels; a board prints the rows of dots it gets. `components/`
has what apps share: `usb_printer` finds the printer and runs transfers, `board` is
what every board does with its app's driver, and `diecut` is the LabelWriters' label
stock. What belongs to one printer goes in its app. An app refuses printers it doesn't list.

```sh
make host-test                              # checks that need no board
make build-all                              # after changing components/
make flash APP=lw550 PORT=/dev/ttyACM0      # the board's UART port
make monitor APP=lw550
```

The printer can be unplugged and replugged at any time; the monitor shows its state
when it changes. An app prints its built-in sample label only if you press `t` there. `tools/sample` draws the samples.

Plug the printer into the board's other USB-C port (the native USB one, GPIO19/20)
and give that port 5 V. Logs go to the UART port. `docs/printing-notes.md` has the
protocols and wiring, `docs/design.md` the design.

## Settings

A board needs its server, token and Wi-Fi networks, in order of preference. Copy
`settings.example.csv` to `settings.<board>.csv` (gitignored), fill it in, and:

```sh
make settings SETTINGS=settings.ptp700.csv   # replaces all the board's settings
```

They are kept in the board's flash, unencrypted, and survive flashing firmware. Never
build them in. The server must be `https://`.

A board asks for a job every 15 s. While it is on a later network it looks for an earlier
one every minute, and if it can't reach the server for 5 minutes it tries another.
