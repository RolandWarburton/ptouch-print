#include "board.h"

#include <stdbool.h>
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

// Waits a while for a key that asks for the sample label, so that a reset alone
// never uses one up.
static bool sample_wanted(void)
{
    ESP_LOGI(TAG, "press t for a sample label, within %d s", KEY_WAIT_MS / 1000);
    for (int waited = 0; waited < KEY_WAIT_MS; waited += 50) {
        vTaskDelay(pdMS_TO_TICKS(50));
        if (getchar() == 't') {
            return true;
        }
    }
    return false;
}

static void print_sample(void)
{
    const sample_t *s = &s_driver->sample;
    const raster_t r = {s->media, s->dots, (s->end - s->start) / raster_row_bytes(s->dots), s->start};
    const char *code = s_driver->print(&r);
    if (*code) {
        ESP_LOGE(TAG, "%s: %s", code, s_driver->error());
    } else {
        ESP_LOGI(TAG, "label printed");
    }
    log_memory("after printing");
}

static void attached(const usb_printer_t *p)
{
    if (s_driver->open(p) != ESP_OK) {
        ESP_LOGE(TAG, "can't open the printer: out of memory");
        return;
    }
    const char *media;
    const char *code = s_driver->state(&media);
    if (*code) {
        ESP_LOGE(TAG, "%s: %s", code, s_driver->error());
    } else {
        ESP_LOGI(TAG, "loaded: %s", media);
        if (sample_wanted()) {
            print_sample();
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
