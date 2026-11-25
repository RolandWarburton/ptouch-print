#include "ptp700.h"

#include <stdio.h>
#include <string.h>

#include "esp_log.h"
#include "esp_timer.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "ptp700_proto.h"
#include "usb_io.h"

static const char *TAG = "ptp700";

#define XFER_BYTES 3200 // a multiple of the 64-byte packet
#define WRITE_MS 20000  // the printer takes no more while its buffer is full
#define READ_MS 1000
#define STATUS_MS 3000
#define PRINT_MS 20000
#define POLL_MS 50 // between reads while the printer is quiet
#define MAX_DRAIN 32

typedef struct {
    usb_printer_t usb;
    usb_transfer_t *t;
    char err[200]; // says what's wrong after a failure
} pt_t;

static const char *fail(pt_t *p, const char *code, const char *why)
{
    snprintf(p->err, sizeof p->err, "%s", why);
    return code;
}

static esp_err_t send(pt_t *p, size_t n) { return usb_io_xfer(p->usb.dev, p->t, p->usb.ep_out, n, WRITE_MS); }

// Reads a status message. While it has nothing to say the printer answers with
// empty packets. ESP_ERR_TIMEOUT when none came in time.
static esp_err_t read_status(pt_t *p, int64_t deadline, pt_status_t *s)
{
    uint8_t msg[PT_STATUS_LEN];
    size_t got = 0;
    while (got < sizeof msg) {
        esp_err_t err = usb_io_xfer(p->usb.dev, p->t, p->usb.ep_in, 64, READ_MS);
        if (err != ESP_OK && err != ESP_ERR_TIMEOUT) {
            return err;
        }
        size_t n = err == ESP_OK ? p->t->actual_num_bytes : 0;
        if (n > sizeof msg - got) {
            return ESP_ERR_INVALID_SIZE;
        }
        memcpy(msg + got, p->t->data_buffer, n);
        got += n;
        if (n == 0) {
            if (esp_timer_get_time() >= deadline) {
                return ESP_ERR_TIMEOUT;
            }
            vTaskDelay(pdMS_TO_TICKS(POLL_MS));
        }
    }
    return pt_parse_status(msg, sizeof msg, s) ? ESP_OK : ESP_ERR_INVALID_RESPONSE;
}

static int64_t after_ms(int ms) { return esp_timer_get_time() + (int64_t)ms * 1000; }

static const char *ask_status(pt_t *p, pt_status_t *s)
{
    // Messages left from an earlier job mustn't pass for the answer.
    for (int i = 0; i < MAX_DRAIN; i++) {
        esp_err_t err = usb_io_xfer(p->usb.dev, p->t, p->usb.ep_in, 64, READ_MS);
        if (err != ESP_OK || p->t->actual_num_bytes == 0) {
            break;
        }
    }
    pt_status_request(p->t->data_buffer);
    if (send(p, PT_REQUEST_LEN) != ESP_OK) {
        return fail(p, CODE_NO_REPLY, "the printer didn't take the status request");
    }
    for (int64_t deadline = after_ms(STATUS_MS);;) {
        if (read_status(p, deadline, s) != ESP_OK) {
            return fail(p, CODE_NO_REPLY, "the printer didn't answer the status request");
        }
        if (s->type == PT_REPLY) {
            return CODE_OK;
        }
    }
}

// Streams the job.
static esp_err_t send_job(pt_t *p, const pt_tape_t *tape, uint8_t media_type, const uint8_t *rows, uint32_t lines)
{
    const size_t row_bytes = raster_row_bytes(tape->dots);
    uint8_t *buf = p->t->data_buffer;
    pt_job_header(buf, tape, media_type, lines);
    size_t n = PT_HEADER_LEN;
    for (uint32_t i = 0; i < lines; i++) {
        uint8_t line[PT_LINE_LEN];
        pt_job_line(line, tape, rows + i * row_bytes);
        size_t fits = XFER_BYTES - n < sizeof line ? XFER_BYTES - n : sizeof line;
        memcpy(buf + n, line, fits);
        n += fits;
        if (n == XFER_BYTES) {
            esp_err_t err = send(p, n);
            if (err != ESP_OK) {
                return err;
            }
            n = sizeof line - fits;
            memcpy(buf, line + fits, n);
        }
    }
    buf[n++] = PT_PRINT; // there is room: n is below XFER_BYTES here
    return send(p, n);
}

// Checks the printer can print, and finds out its tape.
static const char *loaded_tape(pt_t *p, pt_status_t *s, const pt_tape_t **tape)
{
    const char *code = ask_status(p, s);
    if (*code) {
        return code;
    }
    if (s->e1 || s->e2) {
        pt_errors(s, p->err, sizeof p->err);
        return CODE_PRINTER_ERROR;
    }
    *tape = pt_tape(s->tape_mm);
    if (!*tape) {
        snprintf(p->err, sizeof p->err, "unsupported tape width %u mm", s->tape_mm);
        return CODE_UNSUPPORTED_TAPE;
    }
    return CODE_OK;
}

static const char *print(pt_t *p, const raster_t *l)
{
    pt_status_t s;
    const pt_tape_t *tape;
    const char *code = loaded_tape(p, &s, &tape);
    if (*code) {
        return code;
    }
    if (strcmp(l->media, tape->media)) {
        snprintf(p->err, sizeof p->err, "the label is for %s but %s is loaded", l->media, tape->media);
        return CODE_WRONG_MEDIA;
    }
    if (l->dots != tape->dots) {
        return fail(p, CODE_WRONG_HEIGHT, "the label isn't as tall as the tape's printable area");
    }

    esp_err_t err = send_job(p, tape, s.media_type, l->data, l->rows);
    if (err != ESP_OK) {
        snprintf(p->err, sizeof p->err, "lost the printer while printing: %s", esp_err_to_name(err));
        return CODE_UNCONFIRMED;
    }
    for (int64_t deadline = after_ms(PRINT_MS);;) {
        err = read_status(p, deadline, &s);
        if (err == ESP_ERR_INVALID_RESPONSE) {
            continue;
        }
        if (err != ESP_OK) {
            snprintf(p->err, sizeof p->err, "the printer never confirmed the label: %s", esp_err_to_name(err));
            return CODE_UNCONFIRMED;
        }
        ESP_LOGI(TAG, "message: type %u, errors %02x %02x", s.type, s.e1, s.e2);
        if (s.type == PT_COMPLETED) {
            return CODE_OK;
        }
        if (s.type == PT_ERROR) {
            pt_errors(&s, p->err, sizeof p->err);
            if (!p->err[0]) {
                snprintf(p->err, sizeof p->err, "printer rejected the label (error bytes %02x %02x)", s.e1, s.e2);
            }
            return CODE_PRINT_FAILED;
        }
    }
}

// ---- Driver -----------------------------------------------------------------

static pt_t s_p;

static esp_err_t driver_open(const usb_printer_t *usb)
{
    s_p = (pt_t){.usb = *usb};
    return usb_host_transfer_alloc(XFER_BYTES, 0, &s_p.t);
}

static void driver_close(void)
{
    usb_host_transfer_free(s_p.t);
    s_p.t = NULL;
}

static const char *driver_state(const char **media)
{
    pt_status_t s;
    const pt_tape_t *tape;
    const char *code = loaded_tape(&s_p, &s, &tape);
    if (!*code) {
        *media = tape->media;
    }
    return code;
}

static const char *driver_print(const raster_t *l) { return print(&s_p, l); }

static const char *driver_error(void) { return s_p.err; }

extern const uint8_t sample_start[] asm("_binary_sample_rows_start");
extern const uint8_t sample_end[] asm("_binary_sample_rows_end");

static const usb_id_t ids[] = {
    {0x04f9, 0x2061, NULL},
    {0x04f9, 0x2064, CODE_EDITOR_LITE}, // its mass-storage mode
};

const driver_t ptp700_driver = {
    .ids = ids,
    .n_ids = sizeof ids / sizeof ids[0],
    .sample = {"tape-24", 128, sample_start, sample_end},
    .open = driver_open,
    .close = driver_close,
    .state = driver_state,
    .print = driver_print,
    .error = driver_error,
};
