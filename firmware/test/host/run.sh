#!/bin/sh
# Builds the plain C parts of the firmware for this machine and checks them.
set -e
cd "$(dirname "$0")/../.."
mkdir -p test/out

gcc -Wall -Wextra -fsanitize=address,undefined -Icomponents/board/include -o test/out/board_host test/host/board_host.c \
	components/board/proto.c
test/out/board_host

gcc -Wall -Wextra -Iapps/lw550/main -Icomponents/diecut/include -o test/out/dymo550_host test/host/dymo550_host.c \
	apps/lw550/main/dymo550_proto.c components/diecut/media.c
test/out/dymo550_host

gcc -Wall -Wextra -Iapps/ptp700/main -o test/out/ptp700_host test/host/ptp700_host.c apps/ptp700/main/ptp700_proto.c
test/out/ptp700_host
