#include "dymo550_proto.h"

#include <string.h>

static uint32_t u32(const uint8_t *b) { return b[0] | b[1] << 8 | b[2] << 16 | (uint32_t)b[3] << 24; }

bool lw5_parse_status(const uint8_t *b, size_t n, lw5_status_t *s)
{
    if (n != LW5_STATUS_LEN) {
        return false;
    }
    memset(s, 0, sizeof *s);
    s->state = b[0];
    s->job = u32(b + 1);
    s->head = b[8];
    s->bay = b[10];
    memcpy(s->sku, b + 11, LW5_SKU_LEN);
    s->error = u32(b + 23);
    s->labels_left = b[27] | b[28] << 8;
    s->voltage = b[30];
    return true;
}

const char *lw5_problem(const lw5_status_t *s)
{
    switch (s->bay) {
    case 1: return "the label bay is open";
    case 2: return "there are no labels in the printer";
    case 3: return "the labels aren't inserted properly";
    case 5: return "the printer is out of labels";
    case 6: // critically low
    case 7: // low
    case 8: // ok
        break;
    case 9: return "the labels are jammed";
    case 10: return "the labels aren't genuine DYMO labels";
    default: return "the printer can't tell what labels are in it";
    }
    if (s->head == 1) {
        return "the print head is too hot";
    }
    if (s->voltage == 4) {
        return "the print head's voltage is too low";
    }
    if (s->state == LW5_ERROR || s->error) {
        return "the printer reports an error";
    }
    return NULL;
}

void lw5_raster_header(uint8_t out[LW5_HEADER_LEN], uint32_t rows, uint32_t dots)
{
    const uint8_t h[LW5_HEADER_LEN] = {
        0x1b, 0x44, 1 /* bit per dot */, 2 /* align to the label's edge */,
        rows, rows >> 8, rows >> 16, rows >> 24, dots, dots >> 8, dots >> 16, dots >> 24,
    };
    memcpy(out, h, sizeof h);
}
