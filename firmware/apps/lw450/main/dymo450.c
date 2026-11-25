#include "dymo450.h"

#include <stdio.h>
#include <string.h>

#include "esp_log.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "media.h"
#include "sdkconfig.h"
#include "usb_io.h"

static const char *TAG = "dymo450";

#define IO_TIMEOUT_MS 3000
#define ROWS_PER_XFER 40
#define XFER_BYTES (ROWS_PER_XFER * (1 + HEAD_BYTES))
#define SETTLE_MS 3000 // before asking how a label went

#define STATUS_IDLE 0x03
#define STATUS_FAULTS 0xe0 // 0x20 out of labels, 0x40 jam, 0x80 error

static usb_printer_t s_usb;
static usb_transfer_t *s_t;
static char s_err[96];

static const char *fail(const char *code, const char *why)
{
    snprintf(s_err, sizeof s_err, "%s", why);
    return code;
}

static esp_err_t send(size_t len) { return usb_io_xfer(s_usb.dev, s_t, s_usb.ep_out, len, IO_TIMEOUT_MS); }

// ESC A: one status byte.
static esp_err_t ask_status(uint8_t *status)
{
    s_t->data_buffer[0] = 0x1b;
    s_t->data_buffer[1] = 0x41;
    esp_err_t err = send(2);
    if (err == ESP_OK) {
        // IN transfers must be a multiple of the endpoint's max packet size (64
        // at full speed); a short packet ends the reply early.
        err = usb_io_xfer(s_usb.dev, s_t, s_usb.ep_in, 64, IO_TIMEOUT_MS);
    }
    if (err == ESP_OK && s_t->actual_num_bytes != 1) {
        err = ESP_ERR_INVALID_RESPONSE;
    }
    if (err == ESP_OK) {
        *status = s_t->data_buffer[0];
        ESP_LOGI(TAG, "status 0x%02x", *status);
    }
    return err;
}

// Checks the printer is idle. It can't say which labels it has, so they're a setting.
static const char *loaded(const media_t **media)
{
    uint8_t status;
    if (ask_status(&status) != ESP_OK) {
        return fail(CODE_NO_REPLY, "the printer didn't answer");
    }
    if (status != STATUS_IDLE) {
        snprintf(s_err, sizeof s_err, "printer status 0x%02x is not idle (0x%02x)", status, STATUS_IDLE);
        return CODE_PRINTER_ERROR;
    }
    *media = media_find(CONFIG_DYMO_MEDIA_ID);
    return *media ? CODE_OK : fail(CODE_UNSUPPORTED_MEDIA, "the label stock in the settings isn't in the media table");
}

// Streams a label to the printer in batches of rows.
static esp_err_t send_label(const raster_t *l, const media_t *media)
{
    const size_t row_bytes = raster_row_bytes(l->dots);

    // Reset, then set up the page: dot tab 0, bytes per line, normal density,
    // 300x300 dpi, label length in 1/300 in.
    uint8_t *p = s_t->data_buffer;
    memset(p, 0x1b, 100); // resync, in case the printer was mid-command
    p += 100;
    static const uint8_t setup[] = {0x1b, 0x40, 0x1b, 0x42, 0x00, 0x1b, 0x44, HEAD_BYTES, 0x1b, 0x65, 0x1b, 0x68};
    memcpy(p, setup, sizeof setup);
    p += sizeof setup;
    *p++ = 0x1b;
    *p++ = 0x4c;
    *p++ = media->pitch >> 8;
    *p++ = media->pitch & 0xff;
    esp_err_t err = send(p - s_t->data_buffer);

    for (uint32_t y = 0; err == ESP_OK && y < l->rows;) {
        p = s_t->data_buffer;
        for (int n = 0; n < ROWS_PER_XFER && y < l->rows; n++, y++) {
            *p++ = 0x16; // SYN: one raster row follows
            memset(p, 0, HEAD_BYTES);
            memcpy(p, l->data + y * row_bytes, row_bytes);
            p += HEAD_BYTES;
        }
        err = send(p - s_t->data_buffer);
    }
    if (err == ESP_OK) {
        s_t->data_buffer[0] = 0x1b; // ESC E: feed to the tear bar
        s_t->data_buffer[1] = 0x45;
        err = send(2);
    }
    return err;
}

static const char *print(const raster_t *l)
{
    const media_t *media;
    const char *code = loaded(&media);
    if (*code) {
        return code;
    }
    if (strcmp(l->media, media->id)) {
        snprintf(s_err, sizeof s_err, "the label is for %s but %s is loaded", l->media, media->id);
        return CODE_WRONG_MEDIA;
    }
    if (l->dots != media->width || l->rows != media->length) {
        return fail(CODE_WRONG_HEIGHT, "the label isn't the size of the labels loaded");
    }
    esp_err_t err = send_label(l, media);
    if (err != ESP_OK) {
        snprintf(s_err, sizeof s_err, "lost the printer while printing: %s", esp_err_to_name(err));
        return CODE_UNCONFIRMED;
    }
    vTaskDelay(pdMS_TO_TICKS(SETTLE_MS));
    uint8_t status;
    if (ask_status(&status) != ESP_OK) {
        return fail(CODE_UNCONFIRMED, "the printer didn't say how the label went");
    }
    if (status & STATUS_FAULTS) {
        snprintf(s_err, sizeof s_err, "printer status 0x%02x after the label", status);
        return CODE_PRINT_FAILED;
    }
    return CODE_OK;
}

static esp_err_t driver_open(const usb_printer_t *usb)
{
    s_usb = *usb;
    return usb_host_transfer_alloc(XFER_BYTES, 0, &s_t);
}

static void driver_close(void)
{
    usb_host_transfer_free(s_t);
    s_t = NULL;
}

static const char *state(const char **media)
{
    const media_t *m;
    const char *code = loaded(&m);
    if (!*code) {
        *media = m->id;
    }
    return code;
}

static const char *error(void) { return s_err; }

extern const uint8_t sample_start[] asm("_binary_sample_rows_start");
extern const uint8_t sample_end[] asm("_binary_sample_rows_end");

static const usb_id_t ids[] = {{0x0922, 0x0020, NULL}};

const driver_t dymo450_driver = {
    .ids = ids,
    .n_ids = sizeof ids / sizeof ids[0],
    .sample = {"large-address", 400, sample_start, sample_end},
    .open = driver_open,
    .close = driver_close,
    .state = state,
    .print = print,
    .error = error,
};
