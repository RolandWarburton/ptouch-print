IDF_VERSION ?= v5.5
IMAGE       := espressif/idf:$(IDF_VERSION)
PORT        ?= /dev/ttyACM0
DIALOUT_GID := $(shell getent group dialout | cut -d: -f3)

# The project is mounted at its host path so build/compile_commands.json
# resolves for the editor. HOME is writable for idf.py's caches.
DOCKER := docker run --rm -v $(CURDIR):$(CURDIR) -w $(CURDIR) \
	-u $(shell id -u):$(shell id -g) -e HOME=/tmp
SERIAL := --device $(PORT) --group-add $(DIALOUT_GID)

.PHONY: build flash monitor menuconfig shell size host-test clean

# Compile the firmware into build/.
build:
	$(DOCKER) $(IMAGE) bash -c 'idf.py set-target esp32s3 && idf.py build'

# Flash the board over its UART USB-C port: make flash PORT=/dev/ttyACM1
flash:
	$(DOCKER) $(SERIAL) $(IMAGE) idf.py -p $(PORT) flash

# Serial log from the UART port. Ctrl-] exits.
monitor:
	$(DOCKER) -it $(SERIAL) $(IMAGE) idf.py -p $(PORT) monitor

# Where the firmware's RAM and flash go (run make build first).
size:
	$(DOCKER) $(IMAGE) idf.py size

# Draws labels with the label module on this machine, into test/out/ as PBM and PNG.
host-test:
	./test/host/run.sh

menuconfig:
	$(DOCKER) -it $(IMAGE) idf.py menuconfig

shell:
	$(DOCKER) -it $(SERIAL) $(IMAGE) bash

clean:
	rm -rf build sdkconfig sdkconfig.old
