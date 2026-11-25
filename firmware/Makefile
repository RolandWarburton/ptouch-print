IDF_VERSION ?= v5.5
IMAGE       := espressif/idf:$(IDF_VERSION)
PORT        ?= /dev/ttyACM0
APPS        := ptp700 lw450 lw550
# Which firmware: one for each device type.
APP         ?=
DIALOUT_GID := $(shell getent group dialout | cut -d: -f3)

# The project is mounted at its host path so build/compile_commands.json
# resolves for the editor. HOME is writable for idf.py's caches.
DOCKER := docker run --rm -v $(CURDIR):$(CURDIR) -w $(CURDIR)/apps/$(APP) \
	-u $(shell id -u):$(shell id -g) -e HOME=/tmp
SERIAL := --device $(PORT) --group-add $(DIALOUT_GID)

.PHONY: build build-all flash monitor menuconfig shell size host-test clean need-app

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

# Where the firmware's RAM and flash go (run make build first).
size: need-app
	$(DOCKER) $(IMAGE) idf.py size

# Draws labels with the label module on this machine, into test/out/ as PBM and PNG.
host-test:
	./test/host/run.sh

menuconfig: need-app
	$(DOCKER) -it $(IMAGE) idf.py menuconfig

shell: need-app
	$(DOCKER) -it $(SERIAL) $(IMAGE) bash

clean:
	for app in $(APPS); do rm -rf apps/$$app/build apps/$$app/sdkconfig apps/$$app/sdkconfig.old; done
