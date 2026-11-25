// Host-side check of the PT-P700's status parsing, against the rules in
// internal/ptouch/status.go.

#undef NDEBUG
#include <assert.h>
#include <stdio.h>
#include <string.h>

#include "ptp700_proto.h"

int main(void)
{
    uint8_t b[32] = {0x80, [10] = 24, [11] = 1};
    pt_status_t s;
    char errors[160];

    assert(pt_parse_status(b, 32, &s));
    assert(s.tape_mm == 24 && s.media_type == 1 && s.type == PT_REPLY);
    pt_errors(&s, errors, sizeof errors);
    assert(!strcmp(errors, ""));

    b[8] = 0x01;
    b[9] = 0x10;
    b[18] = 0x02;
    assert(pt_parse_status(b, 32, &s) && s.type == PT_ERROR);
    pt_errors(&s, errors, sizeof errors);
    assert(!strcmp(errors, "no tape, cover open"));

    s.e1 = s.e2 = 0xff; // every message fits
    pt_errors(&s, errors, sizeof errors);
    assert(!strcmp(errors + strlen(errors) - 19, "printer overheating"));

    assert(!pt_parse_status(b, 31, &s));
    b[0] = 0;
    assert(!pt_parse_status(b, 32, &s));

    uint8_t r[PT_REQUEST_LEN];
    pt_status_request(r);
    for (int i = 0; i < 100; i++) {
        assert(r[i] == 0);
    }
    assert(!memcmp(r + 100, "\x1b\x40\x1b\x69\x53", 5));

    puts("ptp700: status parsing follows the Go code");
    return 0;
}
