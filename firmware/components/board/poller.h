// Asks the server for this board's next job and reports how it went.
#pragma once

#include <stdbool.h>
#include <stdint.h>

#include "raster.h"

typedef struct {
    int64_t id;
    raster_t raster;
    void *memory; // what raster points into
} job_t;

// Asks for a job, saying what's loaded (media), or why it can't print (problem)
// with media NULL. False when the server couldn't be asked. job->id is 0 when there
// is no job. A job that can't be read is reported here. Free it with job_free.
bool poller_next(const char *media, const char *problem, job_t *job);
void job_free(job_t *job);

// Reports a job's result: code is CODE_OK when it printed. If the report is lost
// the job stays sent on the server: unknown, and never printed again.
void poller_report(const job_t *job, const char *code, const char *error, const char *loaded);
