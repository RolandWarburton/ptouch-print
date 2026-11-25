#include "usb_printer.h"

#include <stdbool.h>

#include "esp_log.h"
#include "freertos/FreeRTOS.h"
#include "freertos/queue.h"
#include "freertos/task.h"
#include "usb/usb_helpers.h"
#include "usb_io.h"

static const char *TAG = "usb_printer";

#define USB_CLASS_PRINTER 0x07

static usb_host_client_handle_t s_client;
static QueueHandle_t s_new_dev;

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

// ---- Attaching --------------------------------------------------------------

static bool drives(const usb_id_t *ids, size_t n, usb_id_t id)
{
    for (size_t i = 0; i < n; i++) {
        if (ids[i].vid == id.vid && ids[i].pid == id.pid) {
            return true;
        }
    }
    return false;
}

static void attach(uint8_t addr, const usb_id_t *ids, size_t n, void (*attached)(const usb_printer_t *))
{
    usb_device_handle_t dev;
    if (usb_host_device_open(s_client, addr, &dev) != ESP_OK) {
        ESP_LOGE(TAG, "can't open device at address %d", addr);
        return;
    }
    const usb_device_desc_t *dd;
    ESP_ERROR_CHECK(usb_host_get_device_descriptor(dev, &dd));
    const usb_config_desc_t *cfg;
    ESP_ERROR_CHECK(usb_host_get_active_config_descriptor(dev, &cfg));
    usb_print_device_descriptor(dd);
    usb_print_config_descriptor(cfg, NULL);

    const usb_id_t id = {dd->idVendor, dd->idProduct};
    if (!drives(ids, n, id)) {
        ESP_LOGE(TAG, "%04x:%04x isn't a printer this firmware drives", id.vid, id.pid);
        goto close;
    }
    printer_if_t p = find_printer_interface(cfg);
    if (!p.found || !p.ep_out || !p.ep_in) {
        ESP_LOGE(TAG, "no printer-class interface with bulk IN and OUT endpoints");
        goto close;
    }
    ESP_LOGI(TAG, "%04x:%04x: printer interface %d alt %d, bulk OUT 0x%02x, bulk IN 0x%02x", id.vid, id.pid, p.intf, p.alt,
             p.ep_out, p.ep_in);
    if (usb_host_interface_claim(s_client, dev, p.intf, p.alt) != ESP_OK) {
        ESP_LOGE(TAG, "can't claim interface %d", p.intf);
        goto close;
    }
    attached(&(usb_printer_t){.dev = dev, .id = id, .ep_out = p.ep_out, .ep_in = p.ep_in});
    usb_host_interface_release(s_client, dev, p.intf);
close:
    usb_host_device_close(s_client, dev);
    ESP_LOGI(TAG, "done; unplug and replug the printer to run it again");
}

void usb_printer_run(const usb_id_t *ids, size_t n, void (*attached)(const usb_printer_t *))
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

    ESP_LOGI(TAG, "waiting for a USB device on the native USB port");
    for (;;) {
        uint8_t addr;
        if (xQueueReceive(s_new_dev, &addr, portMAX_DELAY) == pdTRUE) {
            attach(addr, ids, n, attached);
        }
    }
}
