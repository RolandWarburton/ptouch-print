// The PT-P700's status reply and print job. Plain C with no ESP-IDF calls, so it builds and
// runs on a development machine. test/golden/ptp700 checks it.
#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#define PT_STATUS_LEN 32
#define PT_REQUEST_LEN 105

// Status types, byte 18. The printer sends the others by itself while it prints.
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

// The printable area of a tape width: dots across the tape, and the print head's
// unused pins before them.
typedef struct {
    const char *media; // the server's id
    uint8_t mm, dots, margin;
} pt_tape_t;

// Looks a tape up by its width, or returns NULL when it isn't supported.
const pt_tape_t *pt_tape(uint8_t mm);

#define PT_HEADER_LEN 23
#define PT_LINE_LEN 20
#define PT_PRINT 0x1a // ends a job: print with feed

// Writes what comes before a job's lines. It is only what the PT-P700 needs: the
// extra commands of newer models make it reject the job.
void pt_job_header(uint8_t out[PT_HEADER_LEN], const pt_tape_t *tape, uint8_t media_type, uint32_t lines);

// Writes one raster line from the picture's dots: (dots + 7) / 8 bytes, bit 7 of
// the first byte being the top of the picture.
void pt_job_line(uint8_t out[PT_LINE_LEN], const pt_tape_t *tape, const uint8_t *row);
