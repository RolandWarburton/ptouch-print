// Bring-up for the Brother PT-P700: reports its state, and prints a test label
// only when asked to over the serial port.

#include <stdio.h>

#include "esp_log.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "ptp700.h"

static const char *TAG = "main";

#define KEY_WAIT_MS 30000
#define TEST_TAPE_MM 24

// A framed picture for 24 mm tape with a square in its top left corner.
extern const uint8_t test_start[] asm("_binary_test24_rows_start");
extern const uint8_t test_end[] asm("_binary_test24_rows_end");

static const usb_id_t ids[] = {{0x04f9, 0x2061}};

static void attached(const usb_printer_t *usb)
{
    static pt_t p;
    if (pt_open(&p, usb) != ESP_OK) {
        ESP_LOGE(TAG, "out of memory");
        return;
    }
    pt_status_t s;
    const char *code = pt_status(&p, &s);
    if (*code) {
        ESP_LOGE(TAG, "%s: %s", code, p.err);
    } else {
        char errors[200];
        pt_errors(&s, errors, sizeof errors);
        ESP_LOGI(TAG, "%u mm tape, media type %u, errors: %s", s.tape_mm, s.media_type, errors[0] ? errors : "none");

        ESP_LOGI(TAG, "press t for a test label, within %d s", KEY_WAIT_MS / 1000);
        int key = 0;
        for (int waited = 0; waited < KEY_WAIT_MS && key != 't'; waited += 50) {
            vTaskDelay(pdMS_TO_TICKS(50));
            key = getchar();
        }
        if (key == 't') {
            code = pt_print(&p, TEST_TAPE_MM, test_start, (test_end - test_start) / 16);
            if (*code) {
                ESP_LOGE(TAG, "%s: %s", code, p.err);
            } else {
                ESP_LOGI(TAG, "label printed");
            }
        }
    }
    pt_close(&p);
}

void app_main(void) { usb_printer_run(ids, sizeof ids / sizeof ids[0], attached); }
