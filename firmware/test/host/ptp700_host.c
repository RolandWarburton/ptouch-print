// Host-side check of the PT-P700's status parsing, against what the Go driver it
// replaces made of the same bytes (test/golden/ptp700).

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

    FILE *f = fopen("test/golden/ptp700/errors.txt", "r");
    assert(f);
    char line[256];
    int n = 0;
    for (; fgets(line, sizeof line, f); n++) {
        unsigned e1, e2;
        line[strcspn(line, "\n")] = 0;
        assert(sscanf(line, "%2x %2x", &e1, &e2) == 2);
        s.e1 = e1;
        s.e2 = e2;
        pt_errors(&s, errors, sizeof errors);
        assert(!strcmp(errors, line + 6));
    }
    fclose(f);
    assert(n == 512);

    assert(!pt_parse_status(b, 31, &s));
    b[0] = 0;
    assert(!pt_parse_status(b, 32, &s));

    uint8_t r[PT_REQUEST_LEN];
    pt_status_request(r);
    for (int i = 0; i < 100; i++) {
        assert(r[i] == 0);
    }
    assert(!memcmp(r + 100, "\x1b\x40\x1b\x69\x53", 5));

    puts("ptp700: status parsing matches the Go driver");
    return 0;
}
