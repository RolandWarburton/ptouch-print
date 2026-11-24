// Prints drawn labels on a LabelWriter 550 or 550 Turbo.
#pragma once

#include "dymo550_proto.h"
#include "esp_err.h"
#include "label.h"
#include "usb/usb_host.h"

typedef struct {
    usb_device_handle_t dev;
    uint8_t ep_out, ep_in;
    usb_transfer_t *t;
    char err[96]; // says what's wrong after a failure
} lw5_t;

// Results are the server's result codes. "" is success; "unconfirmed" means a
// label may have printed.
#define LW5_OK ""

esp_err_t lw5_open(lw5_t *p, usb_device_handle_t dev, uint8_t ep_out, uint8_t ep_in);
void lw5_close(lw5_t *p);

// Asks for the status without taking the lock.
const char *lw5_status(lw5_t *p, lw5_status_t *s);

// Finds out which labels are loaded, taking the lock briefly because the
// printer doesn't say without it. *media is set on success.
const char *lw5_loaded_media(lw5_t *p, lw5_status_t *s, const media_t **media);

// Prints one label. It is never retried.
const char *lw5_print(lw5_t *p, const label_t *l);
