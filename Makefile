BIN     := ptouch-print-server
PKG     := ./cmd/ptouch-print-server
RELEASE := CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags '-s -w'

.PHONY: all build test arm64 armv7 armv6 clean

all: test build arm64 armv7 armv6

build:
	go build -o bin/$(BIN) $(PKG)

test:
	go vet ./...
	go test ./...

# 64-bit SBCs (Raspberry Pi 3/4/5 on a 64-bit OS, most current boards).
arm64:
	GOARCH=arm64 $(RELEASE) -o bin/$(BIN)-linux-arm64 $(PKG)

# 32-bit ARMv7 SBCs (older Raspberry Pi OS, BeagleBone, etc).
armv7:
	GOARCH=arm GOARM=7 $(RELEASE) -o bin/$(BIN)-linux-armv7 $(PKG)

# 32-bit ARMv6 boards (Raspberry Pi Zero / Zero W, Pi 1).
armv6:
	GOARCH=arm GOARM=6 $(RELEASE) -o bin/$(BIN)-linux-armv6 $(PKG)

clean:
	rm -rf bin
