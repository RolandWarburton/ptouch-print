// Finds the printer on the ESP32-S3's USB host port, and notices when it goes.
// Knows no printer: the app says which USB ids it drives. All calls are for one task.
#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "usb/usb_host.h"

typedef struct {
    uint16_t vid, pid;
    const char *problem; // NULL for a printer; else the result code to report while it is plugged in
} usb_id_t;

// A claimed printer.
typedef struct {
    usb_device_handle_t dev;
    usb_id_t id;
    uint8_t ep_out, ep_in; // bulk endpoints
} usb_printer_t;

// Starts the USB host. Call once.
void usb_printer_start(const usb_id_t *ids, size_t n);

// Waits for one of the app's printers to be plugged in and claims it, or returns
// NULL when the time is up. Call usb_printer_release when done with it.
const usb_printer_t *usb_printer_wait(int timeout_ms);

// Whether the claimed printer has gone, waiting up to wait_ms for it to.
bool usb_printer_gone(int wait_ms);

// Lets go of the printer, which must have no transfers under way. One that has
// gone must be released before another can be found.
void usb_printer_release(void);

// While there is no printer: the problem of the device that is plugged in, or
// NULL when there's none, or it isn't one the app lists.
const char *usb_printer_problem(void);
