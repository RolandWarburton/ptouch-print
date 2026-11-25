// The PT-P700's status reply. Plain C with no ESP-IDF calls, so it builds and
// runs on a development machine. It mirrors internal/ptouch/status.go.
#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#define PT_STATUS_LEN 32
#define PT_REQUEST_LEN 105

// Status types, byte 18. Only a reply to a request should be PT_REPLY (assumed).
enum { PT_REPLY = 0, PT_COMPLETED = 1, PT_ERROR = 2 };

typedef struct {
    uint8_t tape_mm;    // loaded tape width; 3.5 mm tape reports as 4
    uint8_t media_type; // e.g. 1 laminated; sent back unchanged in the print job
    uint8_t type;
    uint8_t e1, e2; // error bits
} pt_status_t;

// Writes the request: invalidates any half-received job, initialises the
// printer and asks for its status.
void pt_status_request(uint8_t out[PT_REQUEST_LEN]);

// Reads a status message. False if it isn't one.
bool pt_parse_status(const uint8_t *b, size_t n, pt_status_t *s);

// Writes the printer's errors, separated by ", ", or "" when it has none.
void pt_errors(const pt_status_t *s, char *out, size_t cap);
