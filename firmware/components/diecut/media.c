#include "media.h"

#include <stddef.h>
#include <string.h>

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
