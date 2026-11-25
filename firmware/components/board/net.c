#include "net.h"

#include <string.h>
#include <time.h>

#include "esp_event.h"
#include "esp_log.h"
#include "esp_netif.h"
#include "esp_netif_sntp.h"
#include "esp_sntp.h"
#include "esp_task_wdt.h"
#include "esp_timer.h"
#include "esp_wifi.h"
#include "freertos/FreeRTOS.h"
#include "freertos/event_groups.h"
#include "settings.h"

static const char *TAG = "net";

#define JOIN_MS 15000
#define LOOK_MS 60000        // between looks for a network that comes first
#define AVOID_MS (10 * 60000) // how long a network that didn't work is left alone
#define TIME_MS 10000

enum { GOT_IP = 1, LOST = 2 };

static EventGroupHandle_t s_events;
static volatile int s_reason; // why it was last disconnected
static int s_on = -1; // the network it's on, by its place in the settings
static int64_t s_looked;
static int64_t s_avoid[MAX_NETWORKS]; // until when

static int64_t now_ms(void) { return esp_timer_get_time() / 1000; }

static void on_event(void *arg, esp_event_base_t base, int32_t id, void *data)
{
    if (base == WIFI_EVENT && id == WIFI_EVENT_STA_DISCONNECTED) {
        s_reason = ((wifi_event_sta_disconnected_t *)data)->reason;
        xEventGroupClearBits(s_events, GOT_IP);
        xEventGroupSetBits(s_events, LOST);
    } else if (base == IP_EVENT && id == IP_EVENT_STA_GOT_IP) {
        xEventGroupSetBits(s_events, GOT_IP);
    } else if (base == IP_EVENT && id == IP_EVENT_STA_LOST_IP) {
        xEventGroupClearBits(s_events, GOT_IP);
    }
}

void net_start(void)
{
    s_events = xEventGroupCreate();
    ESP_ERROR_CHECK(esp_netif_init());
    ESP_ERROR_CHECK(esp_event_loop_create_default());
    esp_netif_create_default_wifi_sta();
    const wifi_init_config_t cfg = WIFI_INIT_CONFIG_DEFAULT();
    ESP_ERROR_CHECK(esp_wifi_init(&cfg));
    ESP_ERROR_CHECK(esp_wifi_set_storage(WIFI_STORAGE_RAM));
    ESP_ERROR_CHECK(esp_event_handler_register(WIFI_EVENT, ESP_EVENT_ANY_ID, on_event, NULL));
    ESP_ERROR_CHECK(esp_event_handler_register(IP_EVENT, ESP_EVENT_ANY_ID, on_event, NULL));
    ESP_ERROR_CHECK(esp_wifi_set_mode(WIFI_MODE_STA));
    ESP_ERROR_CHECK(esp_wifi_start());
    ESP_ERROR_CHECK(esp_wifi_set_ps(WIFI_PS_NONE)); // it has mains power
    const esp_sntp_config_t sntp = ESP_NETIF_SNTP_DEFAULT_CONFIG("pool.ntp.org");
    ESP_ERROR_CHECK(esp_netif_sntp_init(&sntp)); // keeps trying by itself
}

static void leave(void)
{
    s_on = -1;
    xEventGroupClearBits(s_events, LOST);
    if (esp_wifi_disconnect() == ESP_OK) {
        xEventGroupWaitBits(s_events, LOST, pdTRUE, pdFALSE, pdMS_TO_TICKS(1000));
    }
}

static bool join(int i)
{
    const network_t *n = &settings()->networks[i];
    leave();
    wifi_config_t cfg = {
        .sta =
            {
                // The strongest of the network's access points, not the first found.
                .scan_method = WIFI_ALL_CHANNEL_SCAN,
                .sort_method = WIFI_CONNECT_AP_BY_SIGNAL,
                .threshold.authmode = n->pass[0] ? WIFI_AUTH_WPA2_PSK : WIFI_AUTH_OPEN,
                .sae_pwe_h2e = WPA3_SAE_PWE_BOTH,
            },
    };
    // Either may fill its field, with no NUL.
    memcpy(cfg.sta.ssid, n->ssid, strnlen(n->ssid, sizeof cfg.sta.ssid));
    memcpy(cfg.sta.password, n->pass, strnlen(n->pass, sizeof cfg.sta.password));
    esp_err_t err = esp_wifi_set_config(WIFI_IF_STA, &cfg);
    int absent = 0; // times it wasn't found
    for (int64_t end = now_ms() + JOIN_MS; err == ESP_OK && absent < 2 && now_ms() < end;) {
        esp_task_wdt_reset();
        xEventGroupClearBits(s_events, GOT_IP | LOST);
        err = esp_wifi_connect();
        EventBits_t got = xEventGroupWaitBits(s_events, GOT_IP | LOST, pdFALSE, pdFALSE, pdMS_TO_TICKS(end - now_ms()));
        if (got & GOT_IP) {
            wifi_ap_record_t ap = {0};
            esp_wifi_sta_get_ap_info(&ap);
            ESP_LOGI(TAG, "joined %s, channel %d, %d dBm", n->ssid, ap.primary, ap.rssi);
            s_on = i;
            s_looked = now_ms();
            return true;
        }
        absent += got & LOST && s_reason == WIFI_REASON_NO_AP_FOUND;
        vTaskDelay(pdMS_TO_TICKS(500)); // the join failed early: it may be worth another go
    }
    ESP_LOGW(TAG, "can't join %s: %s, reason %d", n->ssid, esp_err_to_name(err), s_reason);
    leave();
    return false;
}

// Whether a network is in range.
static bool seen(int i)
{
    const wifi_scan_config_t cfg = {.ssid = (uint8_t *)settings()->networks[i].ssid, .show_hidden = true};
    uint16_t n = 0;
    bool ok = esp_wifi_scan_start(&cfg, true) == ESP_OK && esp_wifi_scan_get_ap_num(&n) == ESP_OK && n > 0;
    esp_wifi_clear_ap_list();
    return ok;
}

// Moves to a network that comes before the one it's on, if one has appeared.
static void look_for_better(void)
{
    if (s_on <= 0 || now_ms() - s_looked < LOOK_MS) {
        return;
    }
    s_looked = now_ms();
    for (int i = 0; i < s_on; i++) {
        if (now_ms() >= s_avoid[i] && seen(i)) {
            int was = s_on;
            if (!join(i)) {
                s_avoid[i] = now_ms() + AVOID_MS;
                join(was);
            }
            return;
        }
    }
}

static bool know_time(void)
{
    if (time(NULL) > 1767225600) { // 2026
        return true;
    }
    esp_sntp_restart(); // ask now: it may be waiting after a failure
    esp_netif_sntp_sync_wait(pdMS_TO_TICKS(TIME_MS));
    bool ok = time(NULL) > 1767225600;
    if (!ok) {
        ESP_LOGW(TAG, "no answer from the time server, so certificates can't be checked");
    }
    return ok;
}

bool net_ready(void)
{
    if (s_on >= 0 && !(xEventGroupGetBits(s_events) & GOT_IP)) {
        ESP_LOGW(TAG, "lost %s", net_name());
        s_on = -1;
    }
    look_for_better();
    // Networks that didn't work lately come last.
    for (int pass = 0; pass < 2 && s_on < 0; pass++) {
        for (int i = 0; i < settings()->n_networks && s_on < 0; i++) {
            bool avoided = now_ms() < s_avoid[i];
            if (avoided == (pass == 1)) {
                join(i);
            }
        }
    }
    return s_on >= 0 && know_time();
}

void net_try_another(void)
{
    if (s_on < 0 || settings()->n_networks < 2) {
        return;
    }
    ESP_LOGW(TAG, "leaving %s", net_name());
    s_avoid[s_on] = now_ms() + AVOID_MS;
    leave();
}

const char *net_name(void) { return s_on < 0 ? "" : settings()->networks[s_on].ssid; }
