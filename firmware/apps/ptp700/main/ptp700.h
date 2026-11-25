// Prints pictures on a Brother PT-P700.
#pragma once

#include "codes.h"
#include "ptp700_proto.h"
#include "usb_printer.h"

typedef struct {
    const usb_printer_t *usb;
    usb_transfer_t *t;
    char err[200]; // says what's wrong after a failure
} pt_t;

esp_err_t pt_open(pt_t *p, const usb_printer_t *usb);
void pt_close(pt_t *p);

// Asks for the status. The calls return a result code, CODE_OK on success.
const char *pt_status(pt_t *p, pt_status_t *s);

// Prints a picture of lines rows along the tape, drawn for tape_mm tape. It is
// never retried.
const char *pt_print(pt_t *p, uint8_t tape_mm, const uint8_t *rows, uint32_t lines);
