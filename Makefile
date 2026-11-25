.PHONY: build test clean

# The command server for this machine. Its image is built by the Dockerfile, the
# boards' firmware by firmware/Makefile.
build:
	go build -o bin/ ./cmd/...

test:
	go vet ./...
	go test ./...

clean:
	rm -rf bin
