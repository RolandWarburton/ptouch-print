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
