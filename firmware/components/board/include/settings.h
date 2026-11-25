// The board's settings, from NVS. They are never built in: see settings.example.toml.
#pragma once

#include <stdbool.h>

#define MAX_NETWORKS 4

typedef struct {
    char ssid[33];
    char pass[65];
} network_t;

typedef struct {
    char server[128]; // e.g. https://ptouch.example.com
    char token[128];
    char media[33]; // a LabelWriter 450's label stock, or ""
    network_t networks[MAX_NETWORKS];
    int n_networks; // in order of preference
} settings_t;

// Reads the settings. False, having logged why, when the board can't poll with them.
bool settings_load(void);

const settings_t *settings(void);
