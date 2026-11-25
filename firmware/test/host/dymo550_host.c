// Host-side check of the LabelWriter 550 parsing, against replies captured from
// a 550 Turbo with S0722400 labels in it.

#undef NDEBUG
#include <assert.h>
#include <stdio.h>
#include <string.h>

#include "dymo550_proto.h"
#include "media.h"

int main(void)
{
    static const uint8_t unlocked[32] = {0x05, [9] = 0x64, [10] = 0x08};
    static const uint8_t locked[32] = {[9] = 0x64, 0x08, 'S', '0', '7', '2', '2', '4', '0', '0', [27] = 0x4f};
    static const uint8_t printed[32] = {0x01, 0x01, [9] = 0x64, 0x08, 'S', '0', '7', '2', '2', '4', '0', '0', [27] = 0x4e};
    lw5_status_t s;

    assert(!lw5_parse_status(locked, 31, &s));

    assert(lw5_parse_status(unlocked, 32, &s));
    assert(s.state == LW5_NOT_LOCKED && s.sku[0] == 0 && !lw5_problem(&s));
    assert(!media_find_sku(s.sku));

    assert(lw5_parse_status(locked, 32, &s));
    assert(s.state == LW5_IDLE && s.labels_left == 79 && !lw5_problem(&s));
    assert(media_find_sku(s.sku) == &MEDIA_LARGE_ADDRESS);

    assert(lw5_parse_status(printed, 32, &s));
    assert(s.state == LW5_PRINTING && s.job == 1 && s.labels_left == 78 && !lw5_problem(&s));

    // These states haven't been seen on the printer.
    for (int bay = 0; bay <= 11; bay++) {
        s.bay = bay;
        assert(!lw5_problem(&s) == (bay >= 6 && bay <= 8));
    }
    s.bay = 8;
    s.error = 1;
    assert(lw5_problem(&s));
    s.error = 0;
    s.state = LW5_ERROR;
    assert(lw5_problem(&s));

    // The header that printed a 400 x 950 label.
    static const uint8_t want[12] = {0x1b, 0x44, 1, 2, 0xb6, 0x03, 0, 0, 0x90, 0x01, 0, 0};
    uint8_t h[LW5_HEADER_LEN];
    lw5_raster_header(h, 950, 400);
    assert(!memcmp(h, want, sizeof want));

    puts("dymo550: parsing matches the captured replies");
    return 0;
}
