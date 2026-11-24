#include "usb_io.h"

#include "freertos/FreeRTOS.h"
#include "freertos/semphr.h"

static SemaphoreHandle_t s_done;

void usb_io_init(void) { s_done = xSemaphoreCreateBinary(); }

static void xfer_cb(usb_transfer_t *t) { xSemaphoreGive(s_done); }

// The host library has no transfer timeout, so a stuck endpoint is halted and
// flushed here.
esp_err_t usb_io_xfer(usb_device_handle_t dev, usb_transfer_t *t, uint8_t ep, size_t len, int timeout_ms)
{
    t->device_handle = dev;
    t->bEndpointAddress = ep;
    t->num_bytes = len;
    t->callback = xfer_cb;
    t->context = NULL;
    t->timeout_ms = 0;

    xSemaphoreTake(s_done, 0); // left over from a transfer that timed out
    esp_err_t err = usb_host_transfer_submit(t);
    if (err != ESP_OK) {
        return err;
    }
    if (xSemaphoreTake(s_done, pdMS_TO_TICKS(timeout_ms)) != pdTRUE) {
        usb_host_endpoint_halt(dev, ep);
        usb_host_endpoint_flush(dev, ep);
        xSemaphoreTake(s_done, pdMS_TO_TICKS(1000));
        usb_host_endpoint_clear(dev, ep);
        return ESP_ERR_TIMEOUT;
    }
    return t->status == USB_TRANSFER_STATUS_COMPLETED ? ESP_OK : ESP_FAIL;
}
