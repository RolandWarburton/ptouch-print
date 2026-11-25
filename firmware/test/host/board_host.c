// Host-side check of what a poll says and which rasters are taken.

#undef NDEBUG
#include <assert.h>
#include <stdio.h>
#include <string.h>

#include "codes.h"
#include "proto.h"

int main(void)
{
    char url[128];
    assert(poll_url(url, sizeof url, "https://s.example", "tape-24", "", "Home"));
    assert(!strcmp(url, "https://s.example/board/next?media=tape-24&wifi=Home"));
    assert(poll_url(url, sizeof url, "https://s.example", NULL, "not_connected", ""));
    assert(!strcmp(url, "https://s.example/board/next?media=&problem=not_connected"));
    assert(poll_url(url, sizeof url, "https://s.example", "a", "", "Caf\xc3\xa9 & 1%+"));
    assert(!strcmp(url, "https://s.example/board/next?media=a&wifi=Caf%C3%A9%20%26%201%25%2B"));

    // Whatever the size, it fits or is refused, and never overruns.
    const char *full = "https://s.example/board/next?media=tape-24&wifi=a%20b";
    for (size_t cap = 1; cap < sizeof url; cap++) {
        memset(url, 'x', sizeof url);
        bool ok = poll_url(url, cap, "https://s.example", "tape-24", "", "a b");
        assert(ok == (cap > strlen(full)));
        assert(url[cap] == 'x' && strlen(url) < cap);
        assert(!ok || !strcmp(url, full));
    }

    uint8_t d[1];
    assert(raster_ok(&(raster_t){"tape-24", 128, 10, d}, 160));
    assert(raster_ok(&(raster_t){"tape-12", 70, 3, d}, 27));
    assert(raster_ok(&(raster_t){"tape-24", 128, 3600, d}, 57600));
    assert(!raster_ok(&(raster_t){"tape-24", 128, 10, d}, 159));
    assert(!raster_ok(&(raster_t){"tape-24", 128, 10, d}, 161));
    assert(!raster_ok(&(raster_t){"", 128, 10, d}, 160));
    assert(!raster_ok(&(raster_t){"tape-24", 0, 10, d}, 0));
    assert(!raster_ok(&(raster_t){"tape-24", 128, 0, d}, 0));
    assert(!raster_ok(&(raster_t){"tape-24", 4096, 1000000, d}, 0));
    assert(!raster_ok(&(raster_t){"tape-24", 128, 0x10000000, d}, 0));

    assert(may_have_printed(CODE_UNCONFIRMED));
    assert(!may_have_printed(CODE_PRINT_FAILED) && !may_have_printed(CODE_OK));

    puts("board: poll URLs and raster checks");
    return 0;
}
