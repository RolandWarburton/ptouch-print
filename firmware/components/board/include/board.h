// What every board does, whichever printer it has. The app gives it a driver.
#pragma once

#include "codes.h"
#include "label.h"
#include "usb_printer.h"

// A printer's driver. The calls return a result code, CODE_OK on success.
typedef struct {
    const usb_id_t *ids;
    size_t n_ids;
    const char *sample; // a line for the sample labels
    esp_err_t (*open)(const usb_printer_t *);
    void (*close)(void);
    const char *(*state)(const media_t **); // the loaded media, when it can print
    const char *(*print)(const label_t *);  // one label, never retried
    const char *(*error)(void);             // what's wrong, for people, after a failure
} driver_t;

// Never returns.
void board_run(const driver_t *);
