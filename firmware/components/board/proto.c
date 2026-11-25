#include "proto.h"

#include <stdio.h>
#include <string.h>

#include "codes.h"

// Appends s as a query parameter's value.
static bool add_escaped(char *out, size_t cap, const char *s)
{
    size_t n = strlen(out);
    for (; *s; s++) {
        unsigned char c = *s;
        bool plain = (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || strchr("-_.~", c);
        size_t need = plain ? 1 : 3;
        if (n + need >= cap) {
            return false;
        }
        n += snprintf(out + n, cap - n, plain ? "%c" : "%%%02X", c);
    }
    return true;
}

static bool add(char *out, size_t cap, const char *s)
{
    size_t n = strlen(out);
    return snprintf(out + n, cap - n, "%s", s) < (int)(cap - n);
}

bool poll_url(char *out, size_t cap, const char *server, const char *media, const char *problem, const char *wifi)
{
    out[0] = 0;
    bool ok = add(out, cap, server) && add(out, cap, "/board/next?media=") && add_escaped(out, cap, media ? media : "");
    if (ok && !media) {
        ok = add(out, cap, "&problem=") && add_escaped(out, cap, problem);
    }
    if (ok && wifi && wifi[0]) {
        ok = add(out, cap, "&wifi=") && add_escaped(out, cap, wifi);
    }
    return ok;
}

bool raster_ok(const raster_t *r, size_t size)
{
    return r->media && r->media[0] && r->dots > 0 && r->dots <= 4096 && r->rows > 0 && r->rows <= MAX_RASTER_BYTES &&
           size <= MAX_RASTER_BYTES && raster_row_bytes(r->dots) * r->rows == size;
}

bool may_have_printed(const char *code) { return !strcmp(code, CODE_UNCONFIRMED); }
