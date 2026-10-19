# syntax=docker/dockerfile:1
# The command server. The board's binary is built with make, not here.

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY internal ./internal
COPY cmd ./cmd
ARG TARGETOS TARGETARCH
# Pure Go (no cgo), so the binary is fully static.
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" \
    -o /out/ptouch-command-server ./cmd/ptouch-command-server \
 && mkdir -p /out/data

# Distroless: no shell or package manager, runs as an unprivileged user.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ptouch-command-server /usr/local/bin/
# Owned by the nonroot user so a fresh named volume mounted here is writable.
COPY --from=build --chown=65532:65532 /out/data /data
EXPOSE 8080
USER nonroot
CMD ["ptouch-command-server", "-addr", "0.0.0.0:8080"]
