#include "usb_io.h"

#include "esp_log.h"
#include "esp_system.h"
#include "freertos/FreeRTOS.h"
#include "freertos/semphr.h"

static const char *TAG = "usb_io";

#define CANCEL_MS 2000

static SemaphoreHandle_t s_done;

void usb_io_init(void) { s_done = xSemaphoreCreateBinary(); }

static void xfer_cb(usb_transfer_t *t) { xSemaphoreGive(s_done); }

esp_err_t usb_io_xfer(usb_device_handle_t dev, usb_transfer_t *t, uint8_t ep, size_t len, int timeout_ms)
{
    t->device_handle = dev;
    t->bEndpointAddress = ep;
    t->num_bytes = len;
    t->callback = xfer_cb;
    t->context = NULL;
    t->timeout_ms = 0;

    esp_err_t err = usb_host_transfer_submit(t);
    if (err != ESP_OK) {
        return err;
    }
    bool timed_out = xSemaphoreTake(s_done, pdMS_TO_TICKS(timeout_ms)) != pdTRUE;
    if (timed_out) {
        // Cancel it. It may complete, or the device go, in the meantime.
        usb_host_endpoint_halt(dev, ep);
        usb_host_endpoint_flush(dev, ep);
        if (xSemaphoreTake(s_done, pdMS_TO_TICKS(CANCEL_MS)) != pdTRUE) {
            // The host library still owns the buffer, so nothing is safe any more.
            ESP_LOGE(TAG, "a transfer can't be canceled; restarting");
            esp_restart();
        }
    }
    if (timed_out || t->status != USB_TRANSFER_STATUS_COMPLETED) {
        // A canceled or failed transfer leaves its endpoint halted. Clearing it
        // keeps its data toggle. This fails if the device has gone.
        usb_host_endpoint_clear(dev, ep);
    }
    switch (t->status) {
    case USB_TRANSFER_STATUS_COMPLETED: return ESP_OK;
    case USB_TRANSFER_STATUS_NO_DEVICE: return USB_IO_GONE;
    case USB_TRANSFER_STATUS_CANCELED: return timed_out ? ESP_ERR_TIMEOUT : ESP_FAIL;
    default: ESP_LOGW(TAG, "endpoint 0x%02x: transfer status %d", ep, t->status); return ESP_FAIL;
    }
}
