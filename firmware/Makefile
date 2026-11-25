IDF_VERSION ?= v5.5
IMAGE       := espressif/idf:$(IDF_VERSION)
PORT        ?= /dev/ttyACM0
APPS        := ptp700 lw450 lw550
# Which firmware: one for each device type.
APP         ?=
SETTINGS    ?=
DIALOUT_GID := $(shell getent group dialout | cut -d: -f3)

# The project is mounted at its host path so build/compile_commands.json
# resolves for the editor. HOME is writable for idf.py's caches.
DOCKER := docker run --rm -v $(CURDIR):$(CURDIR) -w $(CURDIR)/apps/$(APP) \
	-u $(shell id -u):$(shell id -g) -e HOME=/tmp
SERIAL := --device $(PORT) --group-add $(DIALOUT_GID)

.PHONY: build build-all flash monitor menuconfig settings shell size host-test clean need-app

need-app:
	@case " $(APPS) " in *" $(APP) "*) [ -n "$(APP)" ];; *) false;; esac || \
		{ echo "say which firmware: make $(firstword $(MAKECMDGOALS)) APP=, one of: $(APPS)" >&2; exit 1; }

# Compile an app's firmware into its build/: make build APP=lw550
build: need-app
	$(DOCKER) $(IMAGE) bash -c 'idf.py set-target esp32s3 && idf.py build'

# Every app, as a change to components/ can break any of them.
build-all:
	for app in $(APPS); do $(MAKE) build APP=$$app || exit 1; done

# Flash the board over its UART USB-C port: make flash APP=lw550 PORT=/dev/ttyACM1
flash: need-app
	$(DOCKER) $(SERIAL) $(IMAGE) idf.py -p $(PORT) flash

# Serial log from the UART port. Ctrl-] exits.
monitor: need-app
	$(DOCKER) -it $(SERIAL) $(IMAGE) idf.py -p $(PORT) monitor

# Write a board's settings, from a file like settings.example.csv. They replace all
# it has: make settings SETTINGS=settings.ptp700.csv
settings:
	@test -f "$(SETTINGS)" || { echo "say which file: make settings SETTINGS=..." >&2; exit 1; }
	docker run --rm -v $(abspath $(SETTINGS)):/settings.csv:ro -u $(shell id -u):$(shell id -g) -e HOME=/tmp $(SERIAL) $(IMAGE) \
		bash -c 'python $$IDF_PATH/components/nvs_flash/nvs_partition_generator/nvs_partition_gen.py generate /settings.csv /tmp/nvs.bin 0x6000 && \
		esptool.py --chip esp32s3 -p $(PORT) write_flash 0x9000 /tmp/nvs.bin'

# Where the firmware's RAM and flash go (run make build first).
size: need-app
	$(DOCKER) $(IMAGE) idf.py size

# Checks of the plain C parts, on this machine.
host-test:
	./test/host/run.sh

menuconfig: need-app
	$(DOCKER) -it $(IMAGE) idf.py menuconfig

shell: need-app
	$(DOCKER) -it $(SERIAL) $(IMAGE) bash

clean:
	for app in $(APPS); do rm -rf apps/$$app/build apps/$$app/sdkconfig apps/$$app/sdkconfig.old; done
