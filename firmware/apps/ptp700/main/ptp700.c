#include "ptp700.h"

#include <stdio.h>
#include <string.h>

#include "esp_log.h"
#include "esp_timer.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "usb_io.h"

static const char *TAG = "ptp700";

#define XFER_BYTES 3200 // a multiple of the 64-byte packet
#define WRITE_MS 20000  // the printer takes no more while its buffer is full
#define READ_MS 1000
#define STATUS_MS 3000
#define PRINT_MS 20000
#define POLL_MS 50 // between reads while the printer is quiet
#define MAX_DRAIN 32

esp_err_t pt_open(pt_t *p, const usb_printer_t *usb)
{
    *p = (pt_t){.usb = usb};
    return usb_host_transfer_alloc(XFER_BYTES, 0, &p->t);
}

void pt_close(pt_t *p)
{
    usb_host_transfer_free(p->t);
    p->t = NULL;
}

static const char *fail(pt_t *p, const char *code, const char *why)
{
    snprintf(p->err, sizeof p->err, "%s", why);
    return code;
}

static esp_err_t send(pt_t *p, size_t n) { return usb_io_xfer(p->usb->dev, p->t, p->usb->ep_out, n, WRITE_MS); }

// Reads a status message. While it has nothing to say the printer answers with
// empty packets. ESP_ERR_TIMEOUT when none came in time.
static esp_err_t read_status(pt_t *p, int64_t deadline, pt_status_t *s)
{
    uint8_t msg[PT_STATUS_LEN];
    size_t got = 0;
    while (got < sizeof msg) {
        esp_err_t err = usb_io_xfer(p->usb->dev, p->t, p->usb->ep_in, 64, READ_MS);
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

const char *pt_status(pt_t *p, pt_status_t *s)
{
    // Messages left from an earlier job mustn't pass for the answer.
    for (int i = 0; i < MAX_DRAIN; i++) {
        esp_err_t err = usb_io_xfer(p->usb->dev, p->t, p->usb->ep_in, 64, READ_MS);
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
    const size_t row_bytes = (tape->dots + 7) / 8;
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

const char *pt_print(pt_t *p, uint8_t tape_mm, const uint8_t *rows, uint32_t lines)
{
    pt_status_t s;
    const char *code = pt_status(p, &s);
    if (*code) {
        return code;
    }
    if (s.e1 || s.e2) {
        pt_errors(&s, p->err, sizeof p->err);
        return CODE_PRINTER_ERROR;
    }
    const pt_tape_t *tape = pt_tape(s.tape_mm);
    if (!tape) {
        snprintf(p->err, sizeof p->err, "unsupported tape width %u mm", s.tape_mm);
        return CODE_UNSUPPORTED_TAPE;
    }
    if (s.tape_mm != tape_mm) {
        snprintf(p->err, sizeof p->err, "the label is for %u mm tape but %u mm is loaded", tape_mm, s.tape_mm);
        return CODE_TAPE_MISMATCH;
    }

    esp_err_t err = send_job(p, tape, s.media_type, rows, lines);
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
