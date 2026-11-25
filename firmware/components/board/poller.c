#include "poller.h"

#include <inttypes.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "cJSON.h"
#include "codes.h"
#include "esp_crt_bundle.h"
#include "esp_http_client.h"
#include "esp_log.h"
#include "mbedtls/base64.h"
#include "net.h"
#include "proto.h"
#include "settings.h"

static const char *TAG = "poller";

#define HTTP_MS 30000
#define MAX_REPLY (96 * 1024) // a raster's 64 KB in base64, and the JSON around it

typedef struct {
    char *buf;
    size_t len;
} reply_t;

// Makes a request, reading the reply into r->buf of MAX_REPLY + 1 bytes when there
// is one. Returns the HTTP status, or -1.
static int request(esp_http_client_method_t method, const char *url, const char *body, reply_t *r)
{
    const esp_http_client_config_t cfg = {
        .url = url,
        .method = method,
        .timeout_ms = HTTP_MS,
        .crt_bundle_attach = esp_crt_bundle_attach,
        .disable_auto_redirect = true, // the token is for this server only
        .buffer_size = 2048,
        .buffer_size_tx = 1024,
    };
    esp_http_client_handle_t c = esp_http_client_init(&cfg);
    if (!c) {
        return -1;
    }
    char auth[sizeof settings()->token + 8];
    snprintf(auth, sizeof auth, "Bearer %s", settings()->token);
    esp_http_client_set_header(c, "Authorization", auth);
    if (body) {
        esp_http_client_set_header(c, "Content-Type", "application/json");
    }

    int status = -1;
    int body_len = body ? strlen(body) : 0;
    esp_err_t err = esp_http_client_open(c, body_len);
    if (err == ESP_OK && body_len && esp_http_client_write(c, body, body_len) != body_len) {
        err = ESP_FAIL;
    }
    if (err == ESP_OK && esp_http_client_fetch_headers(c) < 0) {
        err = ESP_FAIL;
    }
    if (err == ESP_OK) {
        status = esp_http_client_get_status_code(c);
    }
    if (r) {
        r->len = 0;
        r->buf[0] = 0;
    }
    // A 204 has no body, which the client would take for one cut short.
    if (err == ESP_OK && r && status != 204) {
        int n = 0;
        while (r->len < MAX_REPLY && (n = esp_http_client_read(c, r->buf + r->len, MAX_REPLY - r->len)) > 0) {
            r->len += n;
        }
        r->buf[r->len] = 0;
        if (n < 0 || !esp_http_client_is_complete_data_received(c)) {
            ESP_LOGW(TAG, "the reply was cut short, or is too long");
            // A job may have been in it. It stays sent on the server.
            err = ESP_FAIL;
        }
    }
    if (err != ESP_OK) {
        ESP_LOGW(TAG, "no answer from the server: %s", esp_err_to_name(err));
        status = -1;
    }
    esp_http_client_cleanup(c);
    return status;
}

// Reads a job's raster from its JSON.
static bool read_raster(const cJSON *json, job_t *job)
{
    const cJSON *r = cJSON_GetObjectItemCaseSensitive(json, "raster");
    const char *media = cJSON_GetStringValue(cJSON_GetObjectItemCaseSensitive(r, "media"));
    const cJSON *dots = cJSON_GetObjectItemCaseSensitive(r, "dots");
    const cJSON *rows = cJSON_GetObjectItemCaseSensitive(r, "rows");
    const char *data = cJSON_GetStringValue(cJSON_GetObjectItemCaseSensitive(r, "data"));
    if (!media || !cJSON_IsNumber(dots) || !cJSON_IsNumber(rows) || !data || dots->valueint <= 0 || rows->valueint <= 0) {
        return false;
    }
    size_t media_len = strlen(media) + 1, data_len = strlen(data), cap = data_len / 4 * 3 + 3, size;
    char *memory = malloc(media_len + cap);
    if (!memory) {
        return false;
    }
    memcpy(memory, media, media_len);
    uint8_t *bytes = (uint8_t *)memory + media_len;
    job->memory = memory;
    job->raster = (raster_t){.media = memory, .dots = dots->valueint, .rows = rows->valueint, .data = bytes};
    return !mbedtls_base64_decode(bytes, cap, &size, (const uint8_t *)data, data_len) && raster_ok(&job->raster, size);
}

bool poller_next(const char *media, const char *problem, job_t *job)
{
    *job = (job_t){0};
    char url[384];
    if (!poll_url(url, sizeof url, settings()->server, media, problem, net_name())) {
        ESP_LOGE(TAG, "the server's URL is too long");
        return false;
    }
    reply_t reply = {.buf = malloc(MAX_REPLY + 1)};
    if (!reply.buf) {
        ESP_LOGE(TAG, "out of memory");
        return false;
    }
    int status = request(HTTP_METHOD_GET, url, NULL, &reply);
    bool ok = status == 204;
    if (status == 200) {
        cJSON *json = cJSON_ParseWithLength(reply.buf, reply.len);
        const cJSON *id = cJSON_GetObjectItemCaseSensitive(json, "id");
        if (cJSON_IsNumber(id) && id->valuedouble >= 1) {
            ok = true;
            job->id = id->valuedouble;
            if (!read_raster(json, job)) {
                poller_report(job, CODE_BAD_REQUEST, "the board can't read the job", media);
                job_free(job);
                *job = (job_t){0};
            }
        } else {
            ESP_LOGE(TAG, "the server's reply isn't a job");
        }
        cJSON_Delete(json);
    } else if (status > 0 && !ok) {
        ESP_LOGW(TAG, "the server answered %d: %.200s", status, reply.buf);
    }
    free(reply.buf);
    return ok;
}

void job_free(job_t *job)
{
    free(job->memory);
    job->memory = NULL;
}

void poller_report(const job_t *job, const char *code, const char *error, const char *loaded)
{
    cJSON *json = cJSON_CreateObject();
    cJSON_AddBoolToObject(json, "printed", !*code);
    if (*code) {
        cJSON_AddStringToObject(json, "code", code);
        cJSON_AddStringToObject(json, "error", error);
        if (loaded) {
            cJSON_AddStringToObject(json, "loaded_media", loaded);
        }
        if (may_have_printed(code)) {
            cJSON_AddTrueToObject(json, "may_have_printed");
        }
        ESP_LOGW(TAG, "job %" PRId64 " failed: %s: %s", job->id, code, error);
    } else {
        cJSON_AddNumberToObject(json, "length_dots", job->raster.rows);
        ESP_LOGI(TAG, "job %" PRId64 " printed", job->id);
    }
    char *body = cJSON_PrintUnformatted(json);
    cJSON_Delete(json);
    char url[256];
    int n = snprintf(url, sizeof url, "%s/board/jobs/%" PRId64 "/result", settings()->server, job->id);
    int status = body && n < sizeof url ? request(HTTP_METHOD_POST, url, body, NULL) : -1;
    if (status != 204) {
        ESP_LOGE(TAG, "job %" PRId64 "'s result wasn't taken (%d); it is unknown to the server", job->id, status);
    }
    free(body);
}
