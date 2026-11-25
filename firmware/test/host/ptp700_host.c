// Host-side check of the PT-P700's status parsing, against what the Go driver it
// replaces made of the same bytes (test/golden/ptp700).

#undef NDEBUG
#include <assert.h>
#include <stdio.h>
#include <string.h>

#include "ptp700_proto.h"

// Encodes a golden picture and compares the job with the Go driver's.
static void check_job(const char *name, int mm, uint8_t media_type, int lines)
{
    static uint8_t rows[8192], want[8192], got[8192];
    char path[128];
    snprintf(path, sizeof path, "test/golden/ptp700/%s.rows", name);
    FILE *f = fopen(path, "rb");
    assert(f);
    const pt_tape_t *tape = pt_tape(mm);
    const size_t row_bytes = (tape->dots + 7) / 8;
    assert(fread(rows, 1, sizeof rows, f) == lines * row_bytes);
    fclose(f);
    snprintf(path, sizeof path, "test/golden/ptp700/%s.job", name);
    f = fopen(path, "rb");
    assert(f);
    size_t n = fread(want, 1, sizeof want, f);
    fclose(f);

    pt_job_header(got, tape, media_type, lines);
    size_t len = PT_HEADER_LEN;
    for (int i = 0; i < lines; i++, len += PT_LINE_LEN) {
        pt_job_line(got + len, tape, rows + i * row_bytes);
    }
    got[len++] = PT_PRINT;
    assert(len == n && !memcmp(got, want, n));
}

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

    check_job("media1-tape12-black-1", 12, 1, 1);
    check_job("media1-tape12-diagonal-9", 12, 1, 9);
    check_job("media1-tape24-black-1", 24, 1, 1);
    check_job("media1-tape24-blank-1", 24, 1, 1);
    check_job("media1-tape24-diagonal-9", 24, 1, 9);
    check_job("media1-tape24-noise-300", 24, 1, 300);
    check_job("media3-tape12-noise-300", 12, 3, 300);
    assert(!pt_tape(18));

    puts("ptp700: status parsing and print jobs match the Go driver");
    return 0;
}
