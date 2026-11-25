// The board's Wi-Fi and clock. All calls are for one task.
#pragma once

#include <stdbool.h>

void net_start(void);

// Joins a network if the board isn't on one, the first in the settings that works.
// While on a later one, looks now and then for one that comes before it. Takes up
// to a minute. False when it is on none, or doesn't know the time yet.
bool net_ready(void);

// Leaves the network it's on for another, as the server can't be reached over it.
void net_try_another(void);

// The name of the network it's on.
const char *net_name(void);
