#include "board.h"

#include <stdbool.h>
#include <stdio.h>
#include <string.h>

#include "esp_heap_caps.h"
#include "esp_log.h"
#include "esp_task_wdt.h"
#include "esp_timer.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "net.h"
#include "poller.h"
#include "settings.h"

static const char *TAG = "board";

#define POLL_MS 15000
#define UNREACHED_MS (5 * 60000) // how long polls may fail before trying another network
#define FIRST_MS 2000 // for a printer that is plugged in already to be found
#define WATCHDOG_MS 180000

static const driver_t *s_driver;

static int64_t now_ms(void) { return esp_timer_get_time() / 1000; }

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

// The printer's state as of the last look. media is NULL with code saying why.
static const char *s_code, *s_media;
static bool s_open;

static void look_at_printer(void)
{
    s_media = NULL;
    if (s_open) {
        s_code = s_driver->state(&s_media);
        if (*s_code) {
            s_media = NULL;
        }
        report(s_code, *s_code ? s_driver->error() : s_media);
    } else {
        const char *problem = usb_printer_problem();
        s_code = problem ? problem : CODE_NOT_CONNECTED;
        report(s_code, "no printer on the USB port");
    }
}

// Follows the printer being plugged in and unplugged, for up to wait_ms.
static void follow_printer(int wait_ms)
{
    if (s_open) {
        if (usb_printer_gone(wait_ms)) {
            s_driver->close();
            s_open = false;
            usb_printer_release();
            look_at_printer();
        }
        return;
    }
    const usb_printer_t *p = usb_printer_wait(wait_ms);
    if (p && s_driver->open(p) == ESP_OK) {
        s_open = true;
        log_memory("with a printer");
        look_at_printer();
    }
}

static void print_sample(void)
{
    const sample_t *s = &s_driver->sample;
    const raster_t r = {s->media, s->dots, (s->end - s->start) / raster_row_bytes(s->dots), s->start};
    const char *code = s_open ? s_driver->print(&r) : CODE_NOT_CONNECTED;
    if (*code) {
        ESP_LOGE(TAG, "sample: %s: %s", code, s_open ? s_driver->error() : "no printer on the USB port");
    } else {
        ESP_LOGI(TAG, "sample printed");
    }
}

// Asks for a job, prints it and reports. The server is told the state from just
// before; the driver checks it again before printing.
static bool poll(void)
{
    job_t job;
    if (!poller_next(s_media, s_code, &job)) {
        return false;
    }
    if (job.id) {
        esp_task_wdt_reset();
        follow_printer(0); // it may have gone while the server was asked
        const char *code = s_open ? s_driver->print(&job.raster) : CODE_NOT_CONNECTED;
        const char *error = s_open ? s_driver->error() : "no printer on the USB port";
        esp_task_wdt_reset();
        poller_report(&job, code, error, s_media);
        job_free(&job);
        log_memory("after a job");
    }
    return true;
}

void board_run(const driver_t *d)
{
    s_driver = d;
    const esp_task_wdt_config_t wdt = {.timeout_ms = WATCHDOG_MS, .trigger_panic = true};
    ESP_ERROR_CHECK(esp_task_wdt_reconfigure(&wdt));
    ESP_ERROR_CHECK(esp_task_wdt_add(NULL));
    bool online = settings_load();
    usb_printer_start(d->ids, d->n_ids);
    if (online) {
        net_start();
    }
    ESP_LOGI(TAG, "press t for a sample label");
    follow_printer(FIRST_MS);

    int64_t next = 0, reached = now_ms();
    for (;;) {
        esp_task_wdt_reset();
        follow_printer(50);
        if (getchar() == 't') {
            print_sample();
        }
        if (now_ms() < next) {
            continue;
        }
        bool ready = online && net_ready();
        esp_task_wdt_reset();
        follow_printer(0);
        look_at_printer();
        if (ready && poll()) {
            reached = now_ms();
        }
        if (online && now_ms() - reached > UNREACHED_MS) {
            net_try_another();
            reached = now_ms();
        }
        next = now_ms() + POLL_MS; // the same after an error
    }
}
