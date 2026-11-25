#include "usb_printer.h"

#include "esp_log.h"
#include "esp_system.h"
#include "freertos/FreeRTOS.h"
#include "freertos/queue.h"
#include "freertos/task.h"
#include "usb/usb_helpers.h"
#include "usb_io.h"

static const char *TAG = "usb_printer";

#define USB_CLASS_PRINTER 0x07
#define RELEASE_TRIES 40 // of 50 ms

static const usb_id_t *s_ids;
static size_t s_n_ids;
static usb_host_client_handle_t s_client;
static QueueHandle_t s_events;

// The device that is plugged in. It is kept open even if it isn't one of the
// app's printers: only then is the app told when it goes.
static struct {
    usb_device_handle_t dev; // NULL for none
    bool gone;
    bool claimed;
    uint8_t intf;
    const char *problem;
    usb_printer_t printer;
} s;

// ---- USB host plumbing ------------------------------------------------------

typedef struct {
    usb_device_handle_t gone; // the device that has gone, or NULL for a new one
    uint8_t addr;             // of the new one
} event_t;

static void daemon_task(void *arg)
{
    for (;;) {
        uint32_t flags;
        usb_host_lib_handle_events(portMAX_DELAY, &flags);
    }
}

// Runs inside usb_host_client_handle_events: hand off, don't block.
static void client_event_cb(const usb_host_client_event_msg_t *msg, void *arg)
{
    event_t e = {0};
    switch (msg->event) {
    case USB_HOST_CLIENT_EVENT_NEW_DEV: e.addr = msg->new_dev.address; break;
    case USB_HOST_CLIENT_EVENT_DEV_GONE: e.gone = msg->dev_gone.dev_hdl; break;
    default: return;
    }
    if (xQueueSend(s_events, &e, 0) != pdTRUE) {
        ESP_LOGE(TAG, "lost a USB event");
    }
}

static void client_task(void *arg)
{
    for (;;) {
        usb_host_client_handle_events(s_client, portMAX_DELAY);
    }
}

void usb_printer_start(const usb_id_t *ids, size_t n)
{
    s_ids = ids;
    s_n_ids = n;
    s_events = xQueueCreate(8, sizeof(event_t));
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

static const usb_id_t *listed(uint16_t vid, uint16_t pid)
{
    for (size_t i = 0; i < s_n_ids; i++) {
        if (s_ids[i].vid == vid && s_ids[i].pid == pid) {
            return &s_ids[i];
        }
    }
    return NULL;
}

// Opens the device that has been plugged in and, if it is one of the app's
// printers, claims it.
static void attach(uint8_t addr)
{
    esp_err_t err = usb_host_device_open(s_client, addr, &s.dev);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "can't open the device at address %d: %s", addr, esp_err_to_name(err));
        s.dev = NULL;
        return;
    }
    const usb_device_desc_t *dd;
    const usb_config_desc_t *cfg;
    if (usb_host_get_device_descriptor(s.dev, &dd) != ESP_OK || usb_host_get_active_config_descriptor(s.dev, &cfg) != ESP_OK) {
        ESP_LOGE(TAG, "can't read the device's descriptors");
        return;
    }
    const usb_id_t *id = listed(dd->idVendor, dd->idProduct);
    if (!id) {
        ESP_LOGE(TAG, "%04x:%04x isn't a printer this firmware drives", dd->idVendor, dd->idProduct);
        return;
    }
    if (id->problem) {
        ESP_LOGW(TAG, "%04x:%04x: %s", id->vid, id->pid, id->problem);
        s.problem = id->problem;
        return;
    }
    printer_if_t p = find_printer_interface(cfg);
    if (!p.found || !p.ep_out || !p.ep_in) {
        ESP_LOGE(TAG, "no printer-class interface with bulk IN and OUT endpoints");
        usb_print_config_descriptor(cfg, NULL);
        return;
    }
    err = usb_host_interface_claim(s_client, s.dev, p.intf, p.alt);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "can't claim interface %d: %s", p.intf, esp_err_to_name(err));
        return;
    }
    s.claimed = true;
    s.intf = p.intf;
    s.printer = (usb_printer_t){.dev = s.dev, .id = *id, .ep_out = p.ep_out, .ep_in = p.ep_in};
    ESP_LOGI(TAG, "%04x:%04x: printer interface %d alt %d, bulk OUT 0x%02x, bulk IN 0x%02x", id->vid, id->pid, p.intf, p.alt,
             p.ep_out, p.ep_in);
}

void usb_printer_release(void)
{
    if (!s.dev) {
        return;
    }
    // Both are refused for a moment while the last transfers' ends are handled.
    esp_err_t err = ESP_OK;
    for (int i = 0; i < RELEASE_TRIES; i++) {
        if (s.claimed) {
            err = usb_host_interface_release(s_client, s.dev, s.intf);
            s.claimed = err != ESP_OK;
        }
        if (!s.claimed) {
            err = usb_host_device_close(s_client, s.dev);
            if (err == ESP_OK) {
                s = (typeof(s)){0};
                return;
            }
        }
        vTaskDelay(pdMS_TO_TICKS(50));
    }
    // Until it is closed the port finds nothing more.
    ESP_LOGE(TAG, "can't let go of the device: %s; restarting", esp_err_to_name(err));
    esp_restart();
}

// Handles one event, waiting up to wait for it. False when there was none.
static bool handle_event(TickType_t wait)
{
    event_t e;
    if (xQueueReceive(s_events, &e, wait) != pdTRUE) {
        return false;
    }
    if (e.gone) {
        if (e.gone == s.dev) {
            ESP_LOGW(TAG, "unplugged");
            s.gone = true;
        }
    } else if (s.dev) {
        ESP_LOGE(TAG, "a second device, at address %d, is ignored", e.addr);
    } else {
        attach(e.addr);
    }
    return true;
}

const usb_printer_t *usb_printer_wait(int timeout_ms)
{
    TimeOut_t start;
    TickType_t left = pdMS_TO_TICKS(timeout_ms);
    vTaskSetTimeOutState(&start);
    do {
        bool changed = handle_event(left);
        if (s.gone) {
            usb_printer_release();
        }
        if (s.claimed) {
            return &s.printer;
        }
        if (changed) {
            return NULL;
        }
    } while (xTaskCheckForTimeOut(&start, &left) == pdFALSE);
    return NULL;
}

bool usb_printer_gone(int wait_ms)
{
    if (!s.gone) {
        handle_event(pdMS_TO_TICKS(wait_ms));
    }
    return s.gone;
}

const char *usb_printer_problem(void) { return s.problem; }
