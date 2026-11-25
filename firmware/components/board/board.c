#include "board.h"

#include <stdio.h>
#include <string.h>

#include "esp_heap_caps.h"
#include "esp_log.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"

static const char *TAG = "board";

#define POLL_MS 5000

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

// Logs the state the server would be told, when it has changed.
static void report(const char *code, const char *detail)
{
    static char last[160];
    char now[sizeof last];
    snprintf(now, sizeof now, "%s: %s", *code ? code : "ready", detail);
    if (strcmp(now, last)) {
        strcpy(last, now);
        ESP_LOGI(TAG, "state: %s", now);
    }
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
}

// Watches a printer until it goes, printing the sample label when asked to with
// t over the serial port.
static void watch(void)
{
    for (;;) {
        const char *media;
        const char *code = s_driver->state(&media);
        if (usb_printer_gone(0)) {
            return;
        }
        report(code, *code ? s_driver->error() : media);
        for (int waited = 0; waited < POLL_MS; waited += 50) {
            if (usb_printer_gone(50)) {
                return;
            }
            if (getchar() == 't') {
                print_sample();
                break;
            }
        }
    }
}

void board_run(const driver_t *d)
{
    s_driver = d;
    usb_printer_start(d->ids, d->n_ids);
    ESP_LOGI(TAG, "press t for a sample label");
    for (;;) {
        const usb_printer_t *p = usb_printer_wait(POLL_MS);
        if (!p) {
            const char *problem = usb_printer_problem();
            report(problem ? problem : CODE_NOT_CONNECTED, "no printer on the USB port");
            continue;
        }
        if (d->open(p) == ESP_OK) {
            log_memory("with a printer");
            watch();
            d->close();
        } else {
            ESP_LOGE(TAG, "can't open the printer: out of memory");
            usb_printer_gone(POLL_MS);
        }
        usb_printer_release();
        report(CODE_NOT_CONNECTED, "unplugged");
    }
}
