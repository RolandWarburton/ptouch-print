// Bring-up: enumerate a USB device on the ESP32-S3's host port, dump its
// descriptors, find a printer-class interface and its bulk endpoints, claim it
// and ask the printer for its status. An idle 450 then prints a sample label; a
// 550 prints one only when asked to over the serial port.

#include <stdint.h>
#include <stdio.h>
#include <string.h>

#include "dymo550.h"
#include "esp_heap_caps.h"
#include "esp_log.h"
#include "freertos/FreeRTOS.h"
#include "freertos/queue.h"
#include "freertos/task.h"
#include "label.h"
#include "usb/usb_helpers.h"
#include "usb/usb_host.h"
#include "usb_io.h"

static const char *TAG = "dymo";

#define DYMO_VID 0x0922
#define USB_CLASS_PRINTER 0x07
#define IO_TIMEOUT_MS 3000

// The LabelWriter 550 family speaks a different protocol from the 450 family.
static bool is_lw5(uint16_t pid) { return pid == 0x0028 || pid == 0x0029 || pid == 0x002a; }

static usb_host_client_handle_t s_client;
static usb_device_handle_t s_dev;
static QueueHandle_t s_new_dev;

// Logs the free heap: internal RAM and PSRAM now, and the lowest internal RAM has been.
static void log_memory(const char *when)
{
    ESP_LOGI(TAG, "memory %s: internal %u free (lowest %u, largest block %u), PSRAM %u free, stack headroom %u", when,
             (unsigned)heap_caps_get_free_size(MALLOC_CAP_INTERNAL),
             (unsigned)heap_caps_get_minimum_free_size(MALLOC_CAP_INTERNAL),
             (unsigned)heap_caps_get_largest_free_block(MALLOC_CAP_INTERNAL),
             (unsigned)heap_caps_get_free_size(MALLOC_CAP_SPIRAM), (unsigned)uxTaskGetStackHighWaterMark(NULL));
}

// ---- USB host plumbing ------------------------------------------------------

static void daemon_task(void *arg)
{
    for (;;) {
        uint32_t flags;
        usb_host_lib_handle_events(portMAX_DELAY, &flags);
        if (flags & USB_HOST_LIB_EVENT_FLAGS_NO_CLIENTS) {
            ESP_LOGW(TAG, "no clients registered");
        }
    }
}

static void client_event_cb(const usb_host_client_event_msg_t *msg, void *arg)
{
    switch (msg->event) {
    case USB_HOST_CLIENT_EVENT_NEW_DEV:
        // Runs inside usb_host_client_handle_events: hand off, don't block.
        xQueueSend(s_new_dev, &msg->new_dev.address, 0);
        break;
    case USB_HOST_CLIENT_EVENT_DEV_GONE:
        ESP_LOGW(TAG, "device unplugged");
        break;
    default:
        break;
    }
}

static void client_task(void *arg)
{
    for (;;) {
        usb_host_client_handle_events(s_client, portMAX_DELAY);
    }
}

// ---- Bulk transfers ---------------------------------------------------------

static esp_err_t xfer(usb_transfer_t *t, uint8_t ep, size_t len) { return usb_io_xfer(s_dev, t, ep, len, IO_TIMEOUT_MS); }

// Sends cmd on bulk OUT, then reads the reply on bulk IN.
static bool command(usb_transfer_t *t, uint8_t ep_out, uint8_t ep_in, const char *name, const uint8_t *cmd,
                    size_t cmd_len)
{
    memcpy(t->data_buffer, cmd, cmd_len);
    esp_err_t err = xfer(t, ep_out, cmd_len);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "%s: write failed: %s", name, esp_err_to_name(err));
        return false;
    }
    // IN transfers must be a multiple of the endpoint's max packet size (64 at
    // full speed); a short packet ends the reply early.
    err = xfer(t, ep_in, 64);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "%s: no reply: %s", name, esp_err_to_name(err));
        return false;
    }
    ESP_LOGI(TAG, "%s: %d byte reply", name, t->actual_num_bytes);
    ESP_LOG_BUFFER_HEX(TAG, t->data_buffer, t->actual_num_bytes);
    return true;
}

// ---- Printing a label (LabelWriter 450 protocol) -------------------------------

#define PRINT_TEST_LABEL 1 // print a sample label each time the printer is probed

// Streams a label to the printer in batches of rows.
static void print_label(const label_t *l, uint8_t ep_out, uint8_t ep_in, usb_transfer_t *status_xfer)
{
    enum { ROWS_PER_XFER = 40 };
    const int rows = l->media->length;
    usb_transfer_t *t;
    ESP_ERROR_CHECK(usb_host_transfer_alloc(ROWS_PER_XFER * (1 + HEAD_BYTES), 0, &t));

    // Reset, then set up the page: dot tab 0, bytes per line, normal density,
    // 300x300 dpi, label length in 1/300 in.
    uint8_t *p = t->data_buffer;
    memset(p, 0x1b, 100); // resync, in case the printer was mid-command
    p += 100;
    static const uint8_t setup[] = {0x1b, 0x40, 0x1b, 0x42, 0x00, 0x1b, 0x44, HEAD_BYTES, 0x1b, 0x65, 0x1b, 0x68};
    memcpy(p, setup, sizeof setup);
    p += sizeof setup;
    *p++ = 0x1b;
    *p++ = 0x4c;
    *p++ = l->media->pitch >> 8;
    *p++ = l->media->pitch & 0xff;
    esp_err_t err = xfer(t, ep_out, p - t->data_buffer);

    for (int y = 0; err == ESP_OK && y < rows;) {
        p = t->data_buffer;
        for (int n = 0; n < ROWS_PER_XFER && y < rows; n++, y++) {
            *p++ = 0x16; // SYN: one raster row follows
            label_head_row(l, y, p);
            p += HEAD_BYTES;
        }
        err = xfer(t, ep_out, p - t->data_buffer);
    }
    if (err == ESP_OK) {
        t->data_buffer[0] = 0x1b; // ESC E: feed to the tear bar
        t->data_buffer[1] = 0x45;
        err = xfer(t, ep_out, 2);
    }
    usb_host_transfer_free(t);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "label failed: %s", esp_err_to_name(err));
        return;
    }
    ESP_LOGI(TAG, "label sent");
    log_memory("after printing");

    vTaskDelay(pdMS_TO_TICKS(3000));
    static const uint8_t status[] = {0x1b, 0x41};
    command(status_xfer, ep_out, ep_in, "status after printing", status, sizeof status);
}

// Draws the sample label and prints it.
static void print_sample(uint8_t ep_out, uint8_t ep_in, usb_transfer_t *status_xfer)
{
    const media_t *media = media_find(CONFIG_DYMO_MEDIA_ID);
    if (!media) {
        ESP_LOGE(TAG, "unknown label stock \"%s\"", CONFIG_DYMO_MEDIA_ID);
        return;
    }
    label_t l;
    if (label_init(&l, media, ORIENT_PORTRAIT) != LABEL_OK) {
        ESP_LOGE(TAG, "%s", l.err);
        return;
    }
    const char *lines[] = {"ESP32 OK", "Dymo 450"};
    if (label_text(&l, lines, 2, SIZE_MEDIUM, ALIGN_LEFT) != LABEL_OK) {
        ESP_LOGE(TAG, "can't draw the label: %s", l.err);
    } else {
        log_memory("with the label drawn");
        print_label(&l, ep_out, ep_in, status_xfer);
    }
    label_free(&l);
}

// ---- LabelWriter 550 ----------------------------------------------------------

#define KEY_WAIT_MS 30000

// Reports the printer's state and labels, then waits a while for a key that asks
// for a sample label, so that a reset alone never uses one up.
static void probe_lw5(uint8_t ep_out, uint8_t ep_in)
{
    lw5_t p;
    ESP_ERROR_CHECK(lw5_open(&p, s_dev, ep_out, ep_in));
    lw5_status_t s;
    const media_t *media;
    const char *code = lw5_loaded_media(&p, &s, &media);
    if (*code) {
        ESP_LOGE(TAG, "%s: %s", code, p.err);
        lw5_close(&p);
        return;
    }
    ESP_LOGI(TAG, "loaded: %s (%s), %u labels left", media->id, media->name, s.labels_left);

    ESP_LOGI(TAG, "press t for a text sample or b for a barcode sample, within %d s", KEY_WAIT_MS / 1000);
    int key = 0;
    for (int waited = 0; waited < KEY_WAIT_MS && key != 't' && key != 'b'; waited += 50) {
        vTaskDelay(pdMS_TO_TICKS(50));
        key = getchar();
    }
    if (key == 't' || key == 'b') {
        label_t l;
        label_err_t err = label_init(&l, media, key == 't' ? ORIENT_PORTRAIT : ORIENT_LANDSCAPE);
        if (err == LABEL_OK) {
            const char *lines[] = {"ESP32 OK", "Dymo 550"};
            err = key == 't' ? label_text(&l, lines, 2, SIZE_MEDIUM, ALIGN_LEFT) : label_barcode(&l, "ESP32 / Dymo 550");
        }
        if (err != LABEL_OK) {
            ESP_LOGE(TAG, "can't draw the label: %s", l.err);
        } else {
            log_memory("with the label drawn");
            code = lw5_print(&p, &l);
            if (*code) {
                ESP_LOGE(TAG, "%s: %s", code, p.err);
            } else {
                ESP_LOGI(TAG, "label printed");
            }
        }
        label_free(&l);
    }
    lw5_close(&p);
}

// ---- Descriptor walk --------------------------------------------------------

typedef struct {
    bool found;
    uint8_t intf;
    uint8_t alt;
    uint8_t ep_out;
    uint8_t ep_in;
} printer_if_t;

// Walks the raw configuration descriptor for a printer-class interface (class
// 0x07) and records its bulk endpoints.
static printer_if_t find_printer_interface(const usb_config_desc_t *cfg)
{
    printer_if_t p = {0};
    const uint8_t *d = (const uint8_t *)cfg;
    bool in_printer = false;

    for (size_t off = 0; off + 2 <= cfg->wTotalLength && d[off] != 0; off += d[off]) {
        if (d[off + 1] == USB_B_DESCRIPTOR_TYPE_INTERFACE) {
            if (p.found) {
                break; // only the first printer interface
            }
            in_printer = d[off + 5] == USB_CLASS_PRINTER;
            if (in_printer) {
                p.found = true;
                p.intf = d[off + 2];
                p.alt = d[off + 3];
            }
        } else if (d[off + 1] == USB_B_DESCRIPTOR_TYPE_ENDPOINT && in_printer) {
            uint8_t addr = d[off + 2];
            bool bulk = (d[off + 3] & 0x03) == USB_BM_ATTRIBUTES_XFER_BULK;
            if (bulk) {
                if (addr & 0x80) {
                    p.ep_in = addr;
                } else {
                    p.ep_out = addr;
                }
            }
        }
    }
    return p;
}

// ---- Probe ------------------------------------------------------------------

static void probe(uint8_t addr)
{
    if (usb_host_device_open(s_client, addr, &s_dev) != ESP_OK) {
        ESP_LOGE(TAG, "can't open device at address %d", addr);
        return;
    }

    const usb_device_desc_t *dd;
    ESP_ERROR_CHECK(usb_host_get_device_descriptor(s_dev, &dd));
    const usb_config_desc_t *cfg;
    ESP_ERROR_CHECK(usb_host_get_active_config_descriptor(s_dev, &cfg));
    usb_print_device_descriptor(dd);
    usb_print_config_descriptor(cfg, NULL);

    if (dd->idVendor != DYMO_VID) {
        ESP_LOGW(TAG, "VID 0x%04x is not DYMO (0x%04x); not probing", dd->idVendor, DYMO_VID);
        goto close;
    }

    printer_if_t p = find_printer_interface(cfg);
    if (!p.found || !p.ep_out || !p.ep_in) {
        ESP_LOGE(TAG, "no printer-class interface with bulk IN and OUT endpoints");
        goto close;
    }
    ESP_LOGI(TAG, "printer interface %d alt %d, bulk OUT 0x%02x, bulk IN 0x%02x (PID 0x%04x, %s)", p.intf, p.alt,
             p.ep_out, p.ep_in, dd->idProduct, is_lw5(dd->idProduct) ? "LW5 protocol" : "LW450 protocol");

    if (usb_host_interface_claim(s_client, s_dev, p.intf, p.alt) != ESP_OK) {
        ESP_LOGE(TAG, "can't claim interface %d", p.intf);
        goto close;
    }

    usb_transfer_t *t;
    ESP_ERROR_CHECK(usb_host_transfer_alloc(64, 0, &t));

    if (is_lw5(dd->idProduct)) {
        static const uint8_t version[] = {0x1b, 0x56}; // ESC V
        command(t, p.ep_out, p.ep_in, "LW5 engine version (34 bytes expected)", version, sizeof version);
        probe_lw5(p.ep_out, p.ep_in);
    } else {
        static const uint8_t status[] = {0x1b, 0x41}; // ESC A
        bool ok = command(t, p.ep_out, p.ep_in, "LW450 status (1 byte, 0x03 = idle)", status, sizeof status);
        uint8_t state = ok && t->actual_num_bytes == 1 ? t->data_buffer[0] : 0xff;
        static const uint8_t version[] = {0x1b, 0x56}; // ESC V
        command(t, p.ep_out, p.ep_in, "LW450 model/firmware (10 ASCII bytes)", version, sizeof version);
#if PRINT_TEST_LABEL
        if (state == 0x03) {
            print_sample(p.ep_out, p.ep_in, t);
        } else {
            ESP_LOGW(TAG, "printer status 0x%02x is not idle (0x03); not printing", state);
        }
#endif
    }

    usb_host_transfer_free(t);
    usb_host_interface_release(s_client, s_dev, p.intf);
close:
    usb_host_device_close(s_client, s_dev);
    s_dev = NULL;
    ESP_LOGI(TAG, "probe done; unplug and replug the printer to run it again");
}

void app_main(void)
{
    s_new_dev = xQueueCreate(4, sizeof(uint8_t));
    usb_io_init();

    const usb_host_config_t host_cfg = {.skip_phy_setup = false, .intr_flags = ESP_INTR_FLAG_LEVEL1};
    ESP_ERROR_CHECK(usb_host_install(&host_cfg));
    xTaskCreate(daemon_task, "usb_daemon", 4096, NULL, 5, NULL);

    const usb_host_client_config_t client_cfg = {
        .is_synchronous = false,
        .max_num_event_msg = 5,
        .async = {.client_event_callback = client_event_cb, .callback_arg = NULL},
    };
    ESP_ERROR_CHECK(usb_host_client_register(&client_cfg, &s_client));
    xTaskCreate(client_task, "usb_client", 4096, NULL, 5, NULL);

    log_memory("at start");
    ESP_LOGI(TAG, "waiting for a USB device on the native USB port");
    for (;;) {
        uint8_t addr;
        if (xQueueReceive(s_new_dev, &addr, portMAX_DELAY) == pdTRUE) {
            probe(addr);
        }
    }
}
