// What the poll says and gets, apart from the JSON. Plain C with no ESP-IDF calls,
// so it builds and runs on a development machine.
#pragma once

#include <stdbool.h>
#include <stddef.h>

#include "raster.h"

#define MAX_RASTER_BYTES (64 * 1024) // the longest label on tape is 57,600

// Writes the poll's URL. media is the loaded media, or NULL with problem saying
// why it can't print. False if it doesn't fit.
bool poll_url(char *out, size_t cap, const char *server, const char *media, const char *problem, const char *wifi);

// Whether a raster's numbers agree with the size bytes of data it has.
bool raster_ok(const raster_t *r, size_t size);

// Whether a result code means that the label may have come out all the same.
bool may_have_printed(const char *code);
