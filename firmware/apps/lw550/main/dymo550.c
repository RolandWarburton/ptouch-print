#include "dymo550.h"

#include <stdio.h>
#include <string.h>

#include "esp_log.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "media.h"
#include "usb_io.h"

static const char *TAG = "dymo550";

#define XFER_BYTES 3200 // a multiple of the 64-byte packet
#define REPLY_MS 3000
#define PRINT_MS 15000   // the status after a label comes once it has printed
#define LOCK_MS 10000    // the printer can refuse the lock for a few seconds at first
#define LOCK_POLL_MS 250

enum { STATUS_ONLY = 0, TAKE_LOCK = 1 }; // ESC A's parameter

typedef struct {
    usb_printer_t usb;
    usb_transfer_t *t;
    char err[96]; // says what's wrong after a failure
} lw5_t;

static const char *fail(lw5_t *p, const char *code, const char *why)
{
    snprintf(p->err, sizeof p->err, "%s", why);
    return code;
}

static esp_err_t send(lw5_t *p, const uint8_t *b, size_t n)
{
    memcpy(p->t->data_buffer, b, n);
    return usb_io_xfer(p->usb.dev, p->t, p->usb.ep_out, n, PRINT_MS);
}

#define SEND(p, ...) send(p, (const uint8_t[]){__VA_ARGS__}, sizeof((const uint8_t[]){__VA_ARGS__}))

static esp_err_t ask(lw5_t *p, uint8_t lock, int timeout_ms, lw5_status_t *s)
{
    esp_err_t err = SEND(p, 0x1b, 0x41, lock); // ESC A
    if (err == ESP_OK) {
        err = usb_io_xfer(p->usb.dev, p->t, p->usb.ep_in, 64, timeout_ms);
    }
    if (err == ESP_OK && !lw5_parse_status(p->t->data_buffer, p->t->actual_num_bytes, s)) {
        err = ESP_ERR_INVALID_RESPONSE;
    }
    return err;
}

// Ends the job and gives the lock back.
static esp_err_t release(lw5_t *p) { return SEND(p, 0x1b, 0x51); } // ESC Q

// Takes the lock on an idle printer that can print. On failure no lock is held.
static const char *acquire(lw5_t *p, lw5_status_t *s)
{
    for (int waited = 0;; waited += LOCK_POLL_MS) {
        if (ask(p, TAKE_LOCK, REPLY_MS, s) != ESP_OK) {
            release(p); // the lock may have been granted
            return fail(p, CODE_NO_REPLY, "the printer didn't answer");
        }
        bool refused = s->state == LW5_NOT_LOCKED || s->state == LW5_WAKING;
        const char *problem = lw5_problem(s);
        if (s->state == LW5_IDLE && !problem) {
            return CODE_OK;
        }
        if (!refused && problem) {
            release(p);
            return fail(p, CODE_PRINTER_ERROR, problem);
        }
        if (waited >= LOCK_MS) {
            if (!refused) {
                release(p);
            }
            return problem ? fail(p, CODE_PRINTER_ERROR, problem)
                           : fail(p, CODE_BUSY, "the printer is busy, or another computer is using it");
        }
        vTaskDelay(pdMS_TO_TICKS(LOCK_POLL_MS));
    }
}

static const char *media_from(lw5_t *p, const lw5_status_t *s, const media_t **media)
{
    *media = media_find_sku(s->sku);
    if (!*media) {
        snprintf(p->err, sizeof p->err, "labels \"%s\" aren't in the media table", s->sku);
        return CODE_UNSUPPORTED_MEDIA;
    }
    return CODE_OK;
}

// Finds out which labels are loaded, taking the lock briefly because the printer
// doesn't say without it.
static const char *loaded_media(lw5_t *p, lw5_status_t *s, const media_t **media)
{
    *media = NULL;
    const char *code = acquire(p, s);
    if (*code) {
        return code;
    }
    if (release(p) != ESP_OK) {
        return fail(p, CODE_NO_REPLY, "the printer didn't take the end of the job");
    }
    ESP_LOGI(TAG, "%u labels left", s->labels_left);
    return media_from(p, s, media);
}

// Sends everything up to the raster's last row.
static esp_err_t send_label(lw5_t *p, const raster_t *l)
{
    static uint32_t job;
    job++;
    const size_t row_bytes = raster_row_bytes(l->dots);
    uint8_t header[LW5_HEADER_LEN];
    lw5_raster_header(header, l->rows, row_bytes * 8);

    esp_err_t err = SEND(p, 0x1b, 0x73, job, job >> 8, job >> 16, job >> 24); // ESC s: start the job
    if (err == ESP_OK) {
        err = SEND(p, 0x1b, 0x43, 100); // ESC C: normal density
    }
    if (err == ESP_OK) {
        err = SEND(p, 0x1b, 0x68); // ESC h: 300 x 300 dpi
    }
    if (err == ESP_OK) {
        err = SEND(p, 0x1b, 0x6e, 0, 0); // ESC n: first label
    }
    if (err == ESP_OK) {
        err = send(p, header, sizeof header);
    }
    const size_t size = l->rows * row_bytes;
    for (size_t sent = 0; err == ESP_OK && sent < size;) {
        size_t n = size - sent < XFER_BYTES ? size - sent : XFER_BYTES;
        err = send(p, l->data + sent, n);
        sent += n;
    }
    return err;
}

static const char *print(lw5_t *p, const raster_t *l)
{
    lw5_status_t s;
    const char *code = acquire(p, &s);
    if (*code) {
        return code;
    }
    const media_t *loaded;
    code = media_from(p, &s, &loaded);
    if (*code == 0 && strcmp(l->media, loaded->id)) {
        snprintf(p->err, sizeof p->err, "the label is for %s but %s is loaded", l->media, loaded->id);
        code = CODE_WRONG_MEDIA;
    } else if (*code == 0 && (l->dots != loaded->width || l->rows != loaded->length)) {
        code = fail(p, CODE_WRONG_HEIGHT, "the label isn't the size of the labels loaded");
    }
    if (*code) {
        release(p);
        return code;
    }

    esp_err_t err = send_label(p, l);
    if (err == ESP_OK) {
        err = SEND(p, 0x1b, 0x47); // ESC G: feed to the print position
    }
    // The printer takes nothing more until its status is read; skipping this
    // hangs it until it is power-cycled.
    if (err == ESP_OK) {
        err = ask(p, STATUS_ONLY, PRINT_MS, &s);
    }
    if (err != ESP_OK) {
        // If it stopped mid-raster the printer may take this as dots.
        release(p);
        snprintf(p->err, sizeof p->err, "lost the printer while printing: %s", esp_err_to_name(err));
        return CODE_UNCONFIRMED;
    }
    const char *problem = lw5_problem(&s);
    // The last label can leave the bay empty; that doesn't mean it failed.
    if (s.bay == 5 && s.state != LW5_ERROR && !s.error) {
        problem = NULL;
    }
    ESP_LOGI(TAG, "printed: state %u, %u labels left", s.state, s.labels_left);

    err = SEND(p, 0x1b, 0x45); // ESC E: feed to the tear bar
    esp_err_t released = release(p);
    if (problem) {
        return fail(p, CODE_PRINT_FAILED, problem);
    }
    if (err != ESP_OK || released != ESP_OK) {
        return fail(p, CODE_UNCONFIRMED, "the printer didn't take the end of the job");
    }
    return CODE_OK;
}

// ---- Driver -----------------------------------------------------------------

static lw5_t s_p;

static esp_err_t driver_open(const usb_printer_t *usb)
{
    s_p = (lw5_t){.usb = *usb};
    return usb_host_transfer_alloc(XFER_BYTES, 0, &s_p.t);
}

static void driver_close(void)
{
    usb_host_transfer_free(s_p.t);
    s_p.t = NULL;
}

static const char *driver_state(const char **media)
{
    lw5_status_t s;
    const media_t *m;
    const char *code = loaded_media(&s_p, &s, &m);
    if (!*code) {
        *media = m->id;
    }
    return code;
}

static const char *driver_print(const raster_t *l) { return print(&s_p, l); }

static const char *driver_error(void) { return s_p.err; }

extern const uint8_t sample_start[] asm("_binary_sample_rows_start");
extern const uint8_t sample_end[] asm("_binary_sample_rows_end");

static const usb_id_t ids[] = {
    {0x0922, 0x0028, NULL}, // LabelWriter 550, not tried
    {0x0922, 0x0029, NULL}, // LabelWriter 550 Turbo
};

const driver_t dymo550_driver = {
    .ids = ids,
    .n_ids = sizeof ids / sizeof ids[0],
    .sample = {"large-address", 400, sample_start, sample_end},
    .open = driver_open,
    .close = driver_close,
    .state = driver_state,
    .print = driver_print,
    .error = driver_error,
};
