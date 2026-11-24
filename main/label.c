#include "label.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "code128_patterns.h"
#include "font.h"

#define PAD 10          // blank dots kept clear at each edge
#define QUIET 20        // blank dots either side of a barcode, for scanners
#define BAR_HEIGHT 200  // dots
#define CAPTION_GAP 12  // between the bars and their text
#define MAX_MODULES 1024
#define FIRST_CHAR 32
#define LAST_CHAR 126

const media_t MEDIA_LARGE_ADDRESS = {"large-address", "DYMO Large Address S0722400", 400, 950, 1050, "S0722400"};

static const media_t *const MEDIA[] = {&MEDIA_LARGE_ADDRESS};

const media_t *media_find(const char *id)
{
    for (size_t i = 0; i < sizeof MEDIA / sizeof MEDIA[0]; i++) {
        if (!strcmp(MEDIA[i]->id, id)) {
            return MEDIA[i];
        }
    }
    return NULL;
}

const media_t *media_find_sku(const char *sku)
{
    for (size_t i = 0; i < sizeof MEDIA / sizeof MEDIA[0]; i++) {
        if (!strcmp(MEDIA[i]->sku, sku)) {
            return MEDIA[i];
        }
    }
    return NULL;
}

static label_err_t fail(label_t *l, label_err_t code, const char *fmt, ...) __attribute__((format(printf, 3, 4)));

#include <stdarg.h>
static label_err_t fail(label_t *l, label_err_t code, const char *fmt, ...)
{
    va_list ap;
    va_start(ap, fmt);
    vsnprintf(l->err, sizeof l->err, fmt, ap);
    va_end(ap);
    return code;
}

label_err_t label_init(label_t *l, const media_t *media, orient_t orient)
{
    memset(l, 0, sizeof *l);
    l->media = media;
    l->orient = orient;
    l->w = orient == ORIENT_PORTRAIT ? media->width : media->length;
    l->h = orient == ORIENT_PORTRAIT ? media->length : media->width;
    l->stride = (l->w + 7) / 8;
    l->bits = calloc(l->h, l->stride);
    return l->bits ? LABEL_OK : fail(l, LABEL_NO_MEMORY, "out of memory");
}

void label_free(label_t *l)
{
    free(l->bits);
    l->bits = NULL;
}

static void set_dot(label_t *l, int x, int y)
{
    if (x >= 0 && y >= 0 && x < l->w && y < l->h) {
        l->bits[y * l->stride + x / 8] |= 0x80 >> (x % 8);
    }
}

static void fill_rect(label_t *l, int x, int y, int w, int h)
{
    for (int j = y; j < y + h; j++) {
        for (int i = x; i < x + w; i++) {
            set_dot(l, i, j);
        }
    }
}

// Draws one character with its pen position at (x, baseline) and returns its advance.
static int draw_char(label_t *l, const font_t *f, char c, int x, int baseline)
{
    const glyph_t *g = &f->glyphs[c - FIRST_CHAR];
    int row_bytes = (g->w + 7) / 8;
    for (int j = 0; j < g->h; j++) {
        for (int i = 0; i < g->w; i++) {
            if (f->bits[g->off + j * row_bytes + i / 8] & (0x80 >> (i % 8))) {
                set_dot(l, x + g->x + i, baseline + g->y + j);
            }
        }
    }
    return g->adv;
}

static int text_width(const font_t *f, const char *s)
{
    int w = 0;
    for (; *s; s++) {
        w += f->glyphs[*s - FIRST_CHAR].adv;
    }
    return w;
}

// Copies in to out without control characters or surrounding spaces, checking
// that what's left can be drawn.
static label_err_t clean_line(label_t *l, const char *in, char *out, int line_no)
{
    size_t n = 0;
    for (; *in; in++) {
        unsigned char c = *in;
        if (c < FIRST_CHAR || c == 127) {
            continue; // control character
        }
        if (c > LAST_CHAR) {
            return fail(l, LABEL_BAD_TEXT, "line %d has a character that isn't ASCII; only ASCII can be drawn", line_no);
        }
        if (n >= MAX_TEXT_LEN) {
            return fail(l, LABEL_BAD_TEXT, "line %d is longer than %d characters", line_no, MAX_TEXT_LEN);
        }
        out[n++] = c;
    }
    while (n > 0 && out[n - 1] == ' ') {
        n--;
    }
    out[n] = 0;
    size_t lead = strspn(out, " ");
    memmove(out, out + lead, n - lead + 1);
    if (out[0] == 0) {
        return fail(l, LABEL_BAD_TEXT, "line %d is empty", line_no);
    }
    return LABEL_OK;
}

label_err_t label_text(label_t *l, const char *const *lines, int n, text_size_t size, align_t align)
{
    const font_t *f = size == SIZE_LARGE ? &font_large : size == SIZE_MEDIUM ? &font_medium : &font_small;
    if (n < 1 || n > MAX_LINES) {
        return fail(l, LABEL_BAD_TEXT, "a text label has 1 to %d lines", MAX_LINES);
    }

    char clean[n][MAX_TEXT_LEN + 1];
    int inner = l->w - 2 * PAD;
    for (int i = 0; i < n; i++) {
        label_err_t err = clean_line(l, lines[i], clean[i], i + 1);
        if (err != LABEL_OK) {
            return err;
        }
        int w = text_width(f, clean[i]);
        if (w > inner) {
            return fail(l, LABEL_TOO_LONG, "line %d is %d dots wide; the label fits %d", i + 1, w, inner);
        }
    }

    int line_h = f->ascent + f->descent;
    int gap = line_h / 10;
    int block = n * line_h + (n - 1) * gap;
    if (block > l->h - 2 * PAD) {
        return fail(l, LABEL_TOO_LONG, "%d lines are %d dots tall; the label fits %d", n, block, l->h - 2 * PAD);
    }

    int top = (l->h - block) / 2;
    for (int i = 0; i < n; i++) {
        int w = text_width(f, clean[i]);
        int x = align == ALIGN_LEFT ? PAD : align == ALIGN_CENTER ? (l->w - w) / 2 : l->w - PAD - w;
        int baseline = top + i * (line_h + gap) + f->ascent;
        for (const char *c = clean[i]; *c; c++) {
            x += draw_char(l, f, *c, x, baseline);
        }
    }
    return LABEL_OK;
}

// ---- Code 128 ---------------------------------------------------------------
//
// Code B for text and Code C for runs of digits, switching the way the server's
// Go encoder does so the two print the same bars.

enum { START_B = 104, START_C = 105, CODE_B = 100, CODE_C = 99, STOP = 106 };

static bool digits_ahead(const char *s, int n)
{
    for (int i = 0; i < n; i++) {
        if (s[i] < '0' || s[i] > '9') {
            return false; // also covers the end of the string
        }
    }
    return true;
}

static int add_pattern(uint8_t *mod, int n, int cap, int sym)
{
    int bits = sym == STOP ? 13 : 11;
    if (n + bits > cap) {
        return -1;
    }
    for (int b = bits - 1; b >= 0; b--) {
        mod[n++] = (PATTERNS[sym] >> b) & 1;
    }
    return n;
}

int label_code128(const char *text, uint8_t *mod, int cap)
{
    size_t len = strlen(text);
    if (len < 1 || len > MAX_TEXT_LEN) {
        return -1;
    }
    for (const char *c = text; *c; c++) {
        if ((unsigned char)*c < FIRST_CHAR || (unsigned char)*c > LAST_CHAR) {
            return -1;
        }
    }

    int sym[MAX_TEXT_LEN + 2];
    int ns = 0;
    char mode = 0; // 0 = nothing sent yet, 'B' or 'C'
    for (const char *s = text; *s; s++) {
        if (digits_ahead(s, mode == 'C' ? 2 : 4)) {
            if (mode != 'C') {
                sym[ns++] = mode ? CODE_C : START_C;
                mode = 'C';
            }
            sym[ns++] = (s[0] - '0') * 10 + (s[1] - '0');
            s++;
        } else {
            if (mode != 'B') {
                sym[ns++] = mode ? CODE_B : START_B;
                mode = 'B';
            }
            sym[ns++] = *s - FIRST_CHAR;
        }
    }

    int n = 0, sum = 0;
    for (int i = 0; i < ns; i++) {
        sum += i == 0 ? sym[i] : i * sym[i];
        if ((n = add_pattern(mod, n, cap, sym[i])) < 0) {
            return -1;
        }
    }
    if ((n = add_pattern(mod, n, cap, sum % 103)) < 0) {
        return -1;
    }
    return add_pattern(mod, n, cap, STOP);
}

label_err_t label_barcode(label_t *l, const char *text)
{
    uint8_t mod[MAX_MODULES];
    int n = label_code128(text, mod, MAX_MODULES);
    if (n < 0) {
        return fail(l, LABEL_BAD_TEXT, "can't make a Code 128 barcode of that: 1 to %d printable ASCII characters", MAX_TEXT_LEN);
    }

    int avail = l->w - 2 * QUIET;
    int m = n * 3 <= avail ? 3 : 2; // the widest bars that fit scan best
    if (n * m > avail) {
        return fail(l, LABEL_TOO_LONG, "the barcode is %d dots wide; the label fits %d", n * m, avail);
    }
    const font_t *f = &font_caption;
    int cap_w = text_width(f, text);
    if (cap_w > l->w - 2 * PAD) {
        return fail(l, LABEL_TOO_LONG, "the text is %d dots wide; the label fits %d", cap_w, l->w - 2 * PAD);
    }

    int cap_h = f->ascent + f->descent;
    int block = BAR_HEIGHT + CAPTION_GAP + cap_h;
    if (block > l->h - 2 * PAD) {
        return fail(l, LABEL_TOO_LONG, "the barcode is %d dots tall; the label fits %d", block, l->h - 2 * PAD);
    }
    int top = (l->h - block) / 2;
    int left = (l->w - n * m) / 2;
    for (int i = 0; i < n; i++) {
        if (mod[i]) {
            fill_rect(l, left + i * m, top, m, BAR_HEIGHT);
        }
    }
    int x = (l->w - cap_w) / 2;
    for (const char *c = text; *c; c++) {
        x += draw_char(l, f, *c, x, top + BAR_HEIGHT + CAPTION_GAP + f->ascent);
    }
    return LABEL_OK;
}

// ---- Raster rows ------------------------------------------------------------

void label_head_row(const label_t *l, int y, uint8_t out[HEAD_BYTES])
{
    memset(out, 0, HEAD_BYTES);
    if (l->orient == ORIENT_PORTRAIT) {
        memcpy(out, l->bits + y * l->stride, l->stride);
        return;
    }
    // Landscape turns the canvas a quarter turn: its columns become the
    // raster's rows, and its top edge lands at the head's far side.
    int last = l->media->width - 1;
    for (int cy = 0; cy < l->h; cy++) {
        if (l->bits[cy * l->stride + y / 8] & (0x80 >> (y % 8))) {
            int x = last - cy;
            out[x / 8] |= 0x80 >> (x % 8);
        }
    }
}
