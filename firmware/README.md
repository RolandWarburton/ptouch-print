# Board firmware

Firmware for the ESP32-S3 (N16R8) boards, each the USB host of one label printer.
Every device type has its own firmware, built from shared components.

| App | Printers | State |
|---|---|---|
| `apps/lw450` | DYMO LabelWriter 450 | prints; not polling the server yet |
| `apps/lw550` | DYMO LabelWriter 550 (not tried), 550 Turbo | prints; not polling the server yet |
| `apps/ptp700` | Brother PT-P700 | a probe that prints nothing |

`components/` knows no printer: `usb_printer` finds the printer and runs transfers,
`label` draws, and `board` is what every board does with its app's driver. What
belongs to one printer goes in its app. An app refuses printers it doesn't list.

```sh
make host-test                              # checks that need no board
make build-all                              # after changing components/
make flash APP=lw550 PORT=/dev/ttyACM0      # the board's UART port
make monitor APP=lw550
```

The LabelWriter apps print a sample label only if you press `t` (text) or `b`
(barcode) in the monitor within 30 s of the printer being found.

Plug the printer into the board's other USB-C port (the native USB one, GPIO19/20)
and give that port 5 V. Logs go to the UART port. `docs/printing-notes.md` has the
protocols and wiring, `docs/design.md` the design.

Never build secrets in: the Wi-Fi password and the board's token belong in NVS.
