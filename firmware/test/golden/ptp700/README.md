# What the Go driver sent

Written by the Go PT-P700 driver that ran on the Raspberry Pi (`internal/ptouch`, last in
commit 6b5b34f), just before it was removed. It printed on a real PT-P700, so the firmware
must send the same bytes.

- `media<M>-tape<T>-<picture>-<N>.rows`: N raster lines of a picture, in feed order, for
  tape T mm wide. A line is 70 dots in 9 bytes (12 mm) or 128 in 16 (24 mm); bit 7 of the
  first byte is the dot at the top of the picture.
- The `.job` beside it: the print job for those lines with media type M.
- `errors.txt`: error bytes 1 and 2 of a status, and what they mean.

12 mm's margin of 29 dots was never checked on tape.
