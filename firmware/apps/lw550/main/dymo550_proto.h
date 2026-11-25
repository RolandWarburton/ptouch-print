// The LabelWriter 550's status reply and print header. Plain C with no ESP-IDF
// calls, so it builds and runs on a development machine.
#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#define LW5_STATUS_LEN 32
#define LW5_SKU_LEN 12
#define LW5_HEADER_LEN 12

// The printer's state, as byte 0 of the status gives it.
enum {
    LW5_IDLE = 0,
    LW5_PRINTING = 1,
    LW5_ERROR = 2,
    LW5_CANCELED = 3,
    LW5_WAKING = 4,
    LW5_NOT_LOCKED = 5, // also the normal state whenever this host holds no lock
};

typedef struct {
    uint8_t state;
    uint32_t job;
    uint8_t head;             // 0 ok, 1 overheated, 2 unknown
    uint8_t bay;              // the label bay, see lw5_problem
    char sku[LW5_SKU_LEN + 1]; // empty until a lock has been granted
    uint32_t error;           // 0 = none
    uint16_t labels_left;
    uint8_t voltage;          // of the head: 0 unknown, 1 ok, 2 low, 3 critical, 4 too low to print
} lw5_status_t;

// Reads a status reply. False if it isn't one.
bool lw5_parse_status(const uint8_t *b, size_t n, lw5_status_t *s);

// Says why the printer can't print, or returns NULL. Holding no lock or being
// busy isn't a problem.
const char *lw5_problem(const lw5_status_t *s);

// Writes ESC D, the header before a label's raster: rows along the feed of dots
// across the head.
void lw5_raster_header(uint8_t out[LW5_HEADER_LEN], uint32_t rows, uint32_t dots);
