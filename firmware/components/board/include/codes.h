// The server's result codes: why a job failed, or why the printer can't print.
// They match internal/wire, whose tests check this file.
#pragma once

#define CODE_OK "" // not sent

#define CODE_BUSY "busy"
#define CODE_EDITOR_LITE "editor_lite"
#define CODE_NOT_CONNECTED "not_connected"
#define CODE_NO_REPLY "no_reply"
#define CODE_PRINT_FAILED "print_failed" // the printer reported an error after the job was sent
#define CODE_PRINTER_ERROR "printer_error"
#define CODE_TAPE_MISMATCH "tape_mismatch"
#define CODE_UNCONFIRMED "unconfirmed" // may have printed
#define CODE_UNSUPPORTED_MEDIA "unsupported_media"
#define CODE_UNSUPPORTED_TAPE "unsupported_tape"
#define CODE_WRONG_HEIGHT "wrong_height"
#define CODE_WRONG_MEDIA "wrong_media"
