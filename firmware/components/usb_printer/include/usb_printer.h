// Finds the printer on the ESP32-S3's USB host port. Knows no printer: the app
// says which USB ids it drives.
#pragma once

#include <stddef.h>
#include <stdint.h>

#include "usb/usb_host.h"

typedef struct {
    uint16_t vid, pid;
} usb_id_t;

// A claimed printer.
typedef struct {
    usb_device_handle_t dev;
    usb_id_t id;
    uint8_t ep_out, ep_in; // bulk endpoints
} usb_printer_t;

// Calls attached for each of the app's printers that is plugged in, one at a
// time, and never returns. The printer is released when attached returns.
void usb_printer_run(const usb_id_t *ids, size_t n, void (*attached)(const usb_printer_t *));
