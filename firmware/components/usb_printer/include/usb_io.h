// One bulk transfer at a time, with the timeout the USB host library lacks.
#pragma once

#include <stddef.h>
#include <stdint.h>

#include "esp_err.h"
#include "usb/usb_host.h"

// Call once before the first transfer.
void usb_io_init(void);

// Runs t on an endpoint of dev and waits for it. Not for use by two tasks at once.
esp_err_t usb_io_xfer(usb_device_handle_t dev, usb_transfer_t *t, uint8_t ep, size_t len, int timeout_ms);
