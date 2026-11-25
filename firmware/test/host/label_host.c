// Host-side check of the label module. Writes the print head's raster for a
// label as a PBM (P4) on stdout, or prints a barcode's modules.
//
//   label_host text portrait|landscape large|medium|small left|center|right LINE...
//   label_host barcode portrait|landscape TEXT
//   label_host modules TEXT...

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "label.h"

static void write_pbm(const label_t *l)
{
    int rows = l->media->length;
    printf("P4\n%d %d\n", HEAD_DOTS, rows);
    for (int y = 0; y < rows; y++) {
        uint8_t row[HEAD_BYTES];
        label_head_row(l, y, row);
        fwrite(row, 1, HEAD_BYTES, stdout);
    }
}

static int pick(const char *s, const char *const *names, int n)
{
    for (int i = 0; i < n; i++) {
        if (!strcmp(s, names[i])) {
            return i;
        }
    }
    fprintf(stderr, "unknown option %s\n", s);
    exit(2);
}

int main(int argc, char **argv)
{
    if (argc >= 3 && !strcmp(argv[1], "modules")) {
        for (int i = 2; i < argc; i++) {
            uint8_t mod[1024];
            int n = label_code128(argv[i], mod, sizeof mod);
            printf("%s ", argv[i]);
            if (n < 0) {
                printf("error");
            }
            for (int j = 0; j < n; j++) {
                putchar('0' + mod[j]);
            }
            putchar('\n');
        }
        return 0;
    }

    static const char *orients[] = {"portrait", "landscape"};
    static const char *sizes[] = {"large", "medium", "small"};
    static const char *aligns[] = {"left", "center", "right"};
    label_t l;
    label_err_t err;
    if (argc >= 5 && !strcmp(argv[1], "text")) {
        label_init(&l, &MEDIA_LARGE_ADDRESS, pick(argv[2], orients, 2));
        err = label_text(&l, (const char *const *)&argv[5], argc - 5, pick(argv[3], sizes, 3), pick(argv[4], aligns, 3));
    } else if (argc == 4 && !strcmp(argv[1], "barcode")) {
        label_init(&l, &MEDIA_LARGE_ADDRESS, pick(argv[2], orients, 2));
        err = label_barcode(&l, argv[3]);
    } else {
        fprintf(stderr, "usage: see the top of label_host.c\n");
        return 2;
    }
    if (err != LABEL_OK) {
        fprintf(stderr, "error %d: %s\n", err, l.err);
        return 1;
    }
    write_pbm(&l);
    label_free(&l);
    return 0;
}
