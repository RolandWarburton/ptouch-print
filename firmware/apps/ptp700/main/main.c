// A probe for the Brother PT-P700: finds out how its USB behaves from an ESP32
// and prints nothing. It answers step 3 of the migration plan.

#include <inttypes.h>
#include <string.h>

#include "esp_log.h"
#include "esp_timer.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "ptp700_proto.h"
#include "usb_io.h"
#include "usb_printer.h"

static const char *TAG = "ptp700";

#define ROUNDS 100
#define WRITE_MS 3000
#define READ_MS 1000
#define REPLY_MS 3000
#define POLL_MS 50

static const usb_id_t ids[] = {{0x04f9, 0x2061}};

typedef struct {
    int packets, empty, timeouts, errors;
} counts_t;

// Reads one packet into the transfer's buffer. Returns its length, or -1.
static int read_packet(const usb_printer_t *p, usb_transfer_t *t, counts_t *c, int timeout_ms)
{
    esp_err_t err = usb_io_xfer(p->dev, t, p->ep_in, 64, timeout_ms);
    if (err == ESP_OK) {
        t->actual_num_bytes ? c->packets++ : c->empty++;
        return t->actual_num_bytes;
    }
    err == ESP_ERR_TIMEOUT ? c->timeouts++ : c->errors++;
    ESP_LOGD(TAG, "read: %s", esp_err_to_name(err));
    return -1;
}

// Reads whatever is waiting, until the printer has nothing to say.
static void drain(const usb_printer_t *p, usb_transfer_t *t, counts_t *c)
{
    for (int i = 0; i < 8; i++) {
        int n = read_packet(p, t, c, 200);
        if (n <= 0) {
            return;
        }
        ESP_LOGW(TAG, "unasked-for packet of %d bytes", n);
        ESP_LOG_BUFFER_HEX(TAG, t->data_buffer, n);
    }
}

// Asks for the status and reads until 32 bytes have come or the time is up.
static bool ask(const usb_printer_t *p, usb_transfer_t *t, counts_t *c, uint8_t reply[PT_STATUS_LEN])
{
    pt_status_request(t->data_buffer);
    esp_err_t err = usb_io_xfer(p->dev, t, p->ep_out, PT_REQUEST_LEN, WRITE_MS);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "request: %s", esp_err_to_name(err));
        return false;
    }
    int got = 0;
    for (int64_t end = esp_timer_get_time() + REPLY_MS * 1000; got < PT_STATUS_LEN && esp_timer_get_time() < end;) {
        int n = read_packet(p, t, c, READ_MS);
        if (n > PT_STATUS_LEN - got) {
            ESP_LOGW(TAG, "%d bytes is more than a status", got + n);
            n = PT_STATUS_LEN - got;
        }
        if (n > 0) {
            memcpy(reply + got, t->data_buffer, n);
            got += n;
        } else {
            vTaskDelay(pdMS_TO_TICKS(POLL_MS));
        }
    }
    return got == PT_STATUS_LEN;
}

static void attached(const usb_printer_t *p)
{
    usb_transfer_t *t;
    if (usb_host_transfer_alloc(128, 0, &t) != ESP_OK) {
        ESP_LOGE(TAG, "out of memory");
        return;
    }

    counts_t quiet = {0};
    ESP_LOGI(TAG, "---- reading with nothing asked for");
    drain(p, t, &quiet);
    ESP_LOGI(TAG, "quiet reads: %d packets, %d empty, %d timeouts, %d errors", quiet.packets, quiet.empty, quiet.timeouts,
             quiet.errors);

    // Asking straight after a read timed out shows whether the endpoint recovers.
    ESP_LOGI(TAG, "---- %d status requests", ROUNDS);
    counts_t c = {0};
    int good = 0;
    int64_t slowest = 0;
    uint8_t last[PT_STATUS_LEN] = {0};
    for (int i = 0; i < ROUNDS; i++) {
        uint8_t reply[PT_STATUS_LEN];
        pt_status_t s;
        int64_t start = esp_timer_get_time();
        if (!ask(p, t, &c, reply) || !pt_parse_status(reply, sizeof reply, &s)) {
            ESP_LOGE(TAG, "request %d: no status", i);
            continue;
        }
        good++;
        int64_t took = esp_timer_get_time() - start;
        slowest = took > slowest ? took : slowest;
        if (memcmp(reply, last, sizeof last)) {
            char errors[160];
            pt_errors(&s, errors, sizeof errors);
            ESP_LOGI(TAG, "request %d: %u mm tape, media type %u, status type %u, errors: %s", i, s.tape_mm, s.media_type,
                     s.type, errors[0] ? errors : "none");
            ESP_LOG_BUFFER_HEX(TAG, reply, sizeof reply);
            memcpy(last, reply, sizeof last);
        }
        drain(p, t, &c);
    }
    ESP_LOGI(TAG, "%d of %d answered, slowest %" PRId64 " ms; %d packets, %d empty, %d timeouts, %d errors", good, ROUNDS,
             slowest / 1000, c.packets, c.empty, c.timeouts, c.errors);
    usb_host_transfer_free(t);
}

void app_main(void) { usb_printer_run(ids, sizeof ids / sizeof ids[0], attached); }
