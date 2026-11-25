#include "settings.h"

#include <stdio.h>
#include <string.h>

#include "esp_log.h"
#include "nvs.h"
#include "nvs_flash.h"
#include "sdkconfig.h"

static const char *TAG = "settings";

static settings_t s;

const settings_t *settings(void) { return &s; }

// Reads a string, or "" when it isn't set or doesn't fit.
static void get(nvs_handle_t h, const char *key, char *out, size_t cap)
{
    size_t len = cap;
    esp_err_t err = nvs_get_str(h, key, out, &len);
    if (err != ESP_OK) {
        out[0] = 0;
        if (err != ESP_ERR_NVS_NOT_FOUND) {
            ESP_LOGE(TAG, "%s: %s", key, esp_err_to_name(err));
        }
    }
}

bool settings_load(void)
{
    memset(&s, 0, sizeof s);
    esp_err_t err = nvs_flash_init();
    nvs_handle_t h;
    if (err == ESP_OK) {
        err = nvs_open("board", NVS_READONLY, &h);
    }
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "no settings (%s); write them with make settings", esp_err_to_name(err));
        return false;
    }
    get(h, "server", s.server, sizeof s.server);
    get(h, "token", s.token, sizeof s.token);
    get(h, "media", s.media, sizeof s.media);
    for (int i = 0; i < MAX_NETWORKS; i++) {
        char key[8];
        network_t *n = &s.networks[s.n_networks];
        snprintf(key, sizeof key, "ssid%d", i + 1);
        get(h, key, n->ssid, sizeof n->ssid);
        snprintf(key, sizeof key, "pass%d", i + 1);
        get(h, key, n->pass, sizeof n->pass);
        if (n->ssid[0]) {
            s.n_networks++;
        }
    }
    nvs_close(h);

    size_t len = strlen(s.server);
    if (len && s.server[len - 1] == '/') {
        s.server[len - 1] = 0;
    }
    bool secure = !strncmp(s.server, "https://", 8);
#if CONFIG_BOARD_ALLOW_HTTP
    secure = secure || !strncmp(s.server, "http://", 7);
#endif
    if (!secure) {
        ESP_LOGE(TAG, "server must be an https:// URL");
    } else if (!s.token[0]) {
        ESP_LOGE(TAG, "no token");
    } else if (!s.n_networks) {
        ESP_LOGE(TAG, "no Wi-Fi networks");
    } else {
        ESP_LOGI(TAG, "server %s, %d Wi-Fi networks", s.server, s.n_networks);
        return true;
    }
    return false;
}
