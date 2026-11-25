// The label stock a LabelWriter takes. Plain C with no ESP-IDF calls, so it
// builds and runs on a development machine.
#pragma once

#define HEAD_DOTS 672
#define HEAD_BYTES (HEAD_DOTS / 8)

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
