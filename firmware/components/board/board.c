#include "board.h"

#include <stdio.h>

#include "esp_heap_caps.h"
#include "esp_log.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"

static const char *TAG = "board";

#define KEY_WAIT_MS 30000

static const driver_t *s_driver;

// Logs the free heap: internal RAM and PSRAM now, and the lowest internal RAM has been.
static void log_memory(const char *when)
{
    ESP_LOGI(TAG, "memory %s: internal %u free (lowest %u, largest block %u), PSRAM %u free, stack headroom %u", when,
             (unsigned)heap_caps_get_free_size(MALLOC_CAP_INTERNAL),
             (unsigned)heap_caps_get_minimum_free_size(MALLOC_CAP_INTERNAL),
             (unsigned)heap_caps_get_largest_free_block(MALLOC_CAP_INTERNAL),
             (unsigned)heap_caps_get_free_size(MALLOC_CAP_SPIRAM), (unsigned)uxTaskGetStackHighWaterMark(NULL));
}

// Waits a while for a key that asks for a sample label, so that a reset alone
// never uses one up.
static int wait_for_key(void)
{
    ESP_LOGI(TAG, "press t for a text sample or b for a barcode sample, within %d s", KEY_WAIT_MS / 1000);
    for (int waited = 0; waited < KEY_WAIT_MS; waited += 50) {
        vTaskDelay(pdMS_TO_TICKS(50));
        int key = getchar();
        if (key == 't' || key == 'b') {
            return key;
        }
    }
    return 0;
}

static void print_sample(const media_t *media, int key)
{
    label_t l;
    label_err_t err = label_init(&l, media, key == 't' ? ORIENT_PORTRAIT : ORIENT_LANDSCAPE);
    if (err == LABEL_OK) {
        const char *lines[] = {"ESP32 OK", s_driver->sample};
        err = key == 't' ? label_text(&l, lines, 2, SIZE_MEDIUM, ALIGN_LEFT) : label_barcode(&l, s_driver->sample);
    }
    if (err != LABEL_OK) {
        ESP_LOGE(TAG, "can't draw the label: %s", l.err);
    } else {
        log_memory("with the label drawn");
        const char *code = s_driver->print(&l);
        if (*code) {
            ESP_LOGE(TAG, "%s: %s", code, s_driver->error());
        } else {
            ESP_LOGI(TAG, "label printed");
        }
    }
    label_free(&l);
}

static void attached(const usb_printer_t *p)
{
    if (s_driver->open(p) != ESP_OK) {
        ESP_LOGE(TAG, "can't open the printer: out of memory");
        return;
    }
    const media_t *media;
    const char *code = s_driver->state(&media);
    if (*code) {
        ESP_LOGE(TAG, "%s: %s", code, s_driver->error());
    } else {
        ESP_LOGI(TAG, "loaded: %s (%s)", media->id, media->name);
        int key = wait_for_key();
        if (key) {
            print_sample(media, key);
        }
    }
    s_driver->close();
}

void board_run(const driver_t *d)
{
    s_driver = d;
    log_memory("at start");
    usb_printer_run(d->ids, d->n_ids, attached);
}
