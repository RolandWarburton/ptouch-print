// What every board does, whichever printer it has. The app gives it a driver.
#pragma once

#include "codes.h"
#include "raster.h"
#include "usb_printer.h"

// A label built into the firmware, to try a printer with.
typedef struct {
    const char *media;
    uint32_t dots;
    const uint8_t *start, *end;
} sample_t;

// A printer's driver. The calls return a result code, CODE_OK on success.
typedef struct {
    const usb_id_t *ids;
    size_t n_ids;
    sample_t sample;
    esp_err_t (*open)(const usb_printer_t *);
    void (*close)(void);
    const char *(*state)(const char **media); // the loaded media's id, when it can print
    const char *(*print)(const raster_t *);   // one label, never retried
    const char *(*error)(void);               // what's wrong, for people, after a failure
} driver_t;

// Never returns.
void board_run(const driver_t *);
