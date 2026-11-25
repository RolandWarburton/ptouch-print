#include "ptp700_proto.h"

#include <stdio.h>
#include <string.h>

void pt_status_request(uint8_t out[PT_REQUEST_LEN])
{
    static const uint8_t ask[] = {0x1b, 0x40, 0x1b, 0x69, 0x53}; // ESC @, ESC i S
    memset(out, 0, PT_REQUEST_LEN);
    memcpy(out + PT_REQUEST_LEN - sizeof ask, ask, sizeof ask);
}

bool pt_parse_status(const uint8_t *b, size_t n, pt_status_t *s)
{
    if (n < PT_STATUS_LEN || b[0] != 0x80) {
        return false;
    }
    *s = (pt_status_t){.tape_mm = b[10], .media_type = b[11], .type = b[18], .e1 = b[8], .e2 = b[9]};
    return true;
}

typedef struct {
    uint8_t bit;
    const char *msg;
} err_bit_t;

static const err_bit_t error1[] = {{0x01, "no tape"},      {0x02, "tape ran out"},    {0x04, "cutter jammed"},
                                   {0x08, "battery weak"}, {0x40, "printer too hot"}, {0x80, "high voltage adapter"}};
static const err_bit_t error2[] = {
    {0x01, "wrong tape"}, {0x04, "tape cassette changed"}, {0x10, "cover open"}, {0x20, "printer overheating"}};

static void add(char *out, size_t cap, const err_bit_t *bits, size_t n, uint8_t set)
{
    for (size_t i = 0; i < n; i++) {
        if (set & bits[i].bit) {
            size_t len = strlen(out);
            snprintf(out + len, cap - len, "%s%s", len ? ", " : "", bits[i].msg);
        }
    }
}

void pt_errors(const pt_status_t *s, char *out, size_t cap)
{
    out[0] = 0;
    add(out, cap, error1, sizeof error1 / sizeof error1[0], s->e1);
    add(out, cap, error2, sizeof error2 / sizeof error2[0], s->e2);
}

static const pt_tape_t tapes[] = {{"tape-12", 12, 70, 29}, {"tape-24", 24, 128, 0}};

const pt_tape_t *pt_tape(uint8_t mm)
{
    for (size_t i = 0; i < sizeof tapes / sizeof tapes[0]; i++) {
        if (tapes[i].mm == mm) {
            return &tapes[i];
        }
    }
    return NULL;
}

void pt_job_header(uint8_t out[PT_HEADER_LEN], const pt_tape_t *tape, uint8_t media_type, uint32_t lines)
{
    const uint8_t h[PT_HEADER_LEN] = {
        0x1b, 0x69, 0x61, 0x01, // raster mode
        // Print information: without it the printer assumes a default tape and
        // fails with "replace media". Flags: recover | width valid | media valid.
        0x1b, 0x69, 0x7a, 0x86, media_type, tape->mm, 0x00, lines, lines >> 8, lines >> 16, lines >> 24, 0x00, 0x00,
        0x1b, 0x69, 0x4d, 0x40, // auto cut
        0x4d, 0x02,             // TIFF (packbits) compression
    };
    memcpy(out, h, sizeof h);
}

void pt_job_line(uint8_t out[PT_LINE_LEN], const pt_tape_t *tape, const uint8_t *row)
{
    // One packbits literal run: header 15 means "the next 16 bytes as they are".
    memset(out, 0, PT_LINE_LEN);
    out[0] = 'G';
    out[1] = 17;
    out[3] = 15;
    for (int y = 0; y < tape->dots; y++) {
        if (row[y / 8] & (0x80 >> (y % 8))) {
            int pin = tape->margin + y;
            out[4 + pin / 8] |= 0x80 >> (pin % 8);
        }
    }
}
