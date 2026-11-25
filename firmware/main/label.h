// Label drawing: text and Code 128 barcodes on a 1-bit canvas, and the print
// head's raster rows for it. Plain C with no ESP-IDF calls, so it builds and
// runs on a development machine.
#pragma once

#include <stdbool.h>
#include <stdint.h>

#define HEAD_DOTS 672
#define HEAD_BYTES (HEAD_DOTS / 8)
#define MAX_TEXT_LEN 80
#define MAX_LINES 8

// A kind of label stock. width and length are the printable dots across the
// head and along the feed; pitch is the label's length from one to the next.
typedef struct {
    const char *id;
    const char *name;
    int width, length, pitch;
    const char *sku; // what a LabelWriter 550 calls it
} media_t;

// DYMO Large Address Labels, 36 x 89 mm.
extern const media_t MEDIA_LARGE_ADDRESS;

// Looks a media up by id, or returns NULL.
const media_t *media_find(const char *id);

// Looks a media up by the SKU a LabelWriter 550 reports, or returns NULL.
const media_t *media_find_sku(const char *sku);

typedef enum {
    ORIENT_PORTRAIT,  // the label's short side is the text's width
    ORIENT_LANDSCAPE, // the long side is; turn the label a quarter turn left to read it
} orient_t;

typedef enum { SIZE_LARGE, SIZE_MEDIUM, SIZE_SMALL } text_size_t;
typedef enum { ALIGN_LEFT, ALIGN_CENTER, ALIGN_RIGHT } align_t;

// Why a label can't be drawn. They match the result codes the board reports.
typedef enum { LABEL_OK, LABEL_BAD_TEXT, LABEL_TOO_LONG, LABEL_NO_MEMORY } label_err_t;

typedef struct {
    const media_t *media;
    orient_t orient;
    int w, h; // canvas size in dots
    int stride;
    uint8_t *bits;
    char err[96]; // says what's wrong after a failure
} label_t;

// Allocates a blank canvas. Call label_free when done.
label_err_t label_init(label_t *l, const media_t *media, orient_t orient);
void label_free(label_t *l);

// Draws lines of text, centred vertically. Control characters are removed and
// each line trimmed; only printable ASCII can be drawn.
label_err_t label_text(label_t *l, const char *const *lines, int n, text_size_t size, align_t align);

// Draws a Code 128 barcode of text with the text beneath it, centred.
label_err_t label_barcode(label_t *l, const char *text);

// Fills out with the print head's dots for raster row y (0 to media->length - 1),
// where the label's edge is at dot 0.
void label_head_row(const label_t *l, int y, uint8_t out[HEAD_BYTES]);

// Writes the Code 128 modules (1 = bar) of text into mod, returning how many, or
// -1 if text can't be encoded or doesn't fit in cap.
int label_code128(const char *text, uint8_t *mod, int cap);
