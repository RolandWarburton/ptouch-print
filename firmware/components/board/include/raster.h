// A drawn label, as the server sends it (wire.Raster). Plain C.
#pragma once

#include <stddef.h>
#include <stdint.h>

// Rows of dots across the print head, in feed order. A row is raster_row_bytes
// long; bit 7 of its first byte is the first dot, and 1 is black.
typedef struct {
    const char *media; // the only media it may be printed on
    uint32_t dots, rows;
    const uint8_t *data;
} raster_t;

static inline size_t raster_row_bytes(uint32_t dots) { return (dots + 7) / 8; }
