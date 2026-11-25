#!/bin/sh
# Builds the label module for this machine, checks its Code 128 bars against the
# Go encoder the server uses, and draws sample labels into test/out/.
set -e
cd "$(dirname "$0")/../.."
mkdir -p test/out
LABEL="-Icomponents/label/include components/label/label.c components/label/font_data.c"
gcc -Wall -Wextra -o test/out/label_host test/host/label_host.c $LABEL

gcc -Wall -Wextra -Iapps/lw550/main -o test/out/dymo550_host test/host/dymo550_host.c apps/lw550/main/dymo550_proto.c $LABEL
test/out/dymo550_host

gcc -Wall -Wextra -Iapps/ptp700/main -o test/out/ptp700_host test/host/ptp700_host.c apps/ptp700/main/ptp700_proto.c
test/out/ptp700_host

SAMPLES="1234001234 00000000 TG17573 12345678 1234567 123 Hello_123 ab12345cd 99887766abcd"
(cd tools/code128ref && go run . $SAMPLES) > test/out/code128.expected
test/out/label_host modules $SAMPLES > test/out/code128.actual
diff test/out/code128.expected test/out/code128.actual && echo "code128: bars match the Go encoder"

png() { (cd tools/pbm2png && go run . "$@"); }
draw() { # name args...
	name=$1; shift
	test/out/label_host "$@" > test/out/$name.pbm
}
draw text-portrait text portrait medium left "ESP32 OK" "Dymo 450"
draw text-landscape text landscape large center "Shelf B4 - Cables"
draw text-small text portrait small left "Line one" "Line two" "Line three"
draw barcode-portrait barcode portrait TG17573
draw barcode-landscape barcode landscape "TG17573 / Dymo"
for f in text-portrait text-small barcode-portrait; do png -crop 420 < test/out/$f.pbm > test/out/$f.png; done
for f in text-landscape barcode-landscape; do png -crop 420 -ccw < test/out/$f.pbm > test/out/$f.png; done
echo "drawn: test/out/*.png"
