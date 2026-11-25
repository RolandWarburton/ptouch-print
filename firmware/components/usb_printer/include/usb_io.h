// One bulk transfer at a time, with the timeout the USB host library lacks.
#pragma once

#include <stddef.h>
#include <stdint.h>

#include "esp_err.h"
#include "usb/usb_host.h"

#define USB_IO_GONE ESP_ERR_NOT_FOUND // the device has been unplugged

// Call once before the first transfer.
void usb_io_init(void);

// Runs t on an endpoint of dev and waits for it. Not for use by two tasks at once.
// ESP_ERR_TIMEOUT when it took too long, USB_IO_GONE, or another error; after any
// of them no transfer is under way and, unless it has gone, the endpoint can be
// used again.
esp_err_t usb_io_xfer(usb_device_handle_t dev, usb_transfer_t *t, uint8_t ep, size_t len, int timeout_ms);
