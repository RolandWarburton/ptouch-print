# dymo-print

Prints labels on a DYMO LabelWriter 450 or 550 from an ESP32-S3 (N16R8) acting
as a USB host. See `docs/printing-notes.md` for the protocols and wiring.

## Bring-up firmware

`main/main.c` waits for a USB device, prints its descriptors, finds the
printer-class interface and its bulk endpoints, claims it, and asks the
printer for its status. An idle 450 then prints a sample label. A 550 prints one
only if you press `t` (text) or `b` (barcode) in the serial monitor.

```sh
idf.py set-target esp32s3
idf.py build
idf.py -p /dev/ttyUSB0 flash monitor   # the board's UART port
```

Plug the printer into the board's other USB-C port (the native USB one, GPIO19/20)
and give that port 5 V. Logs go to the UART port.
