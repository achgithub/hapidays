# Build-only image: compiles hapidays for every target OS/arch and leaves
# the binaries in /out. Nothing here runs at container runtime — this is
# purely a cross-compiler since there's no local Go toolchain.
FROM golang:1.23-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
ENV CGO_ENABLED=0
RUN set -eux; \
  for target in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64; do \
    GOOS=${target%/*}; GOARCH=${target#*/}; \
    out=/out/hapidays-${GOOS}-${GOARCH}; \
    [ "$GOOS" = "windows" ] && out="${out}.exe"; \
    GOOS=$GOOS GOARCH=$GOARCH go build -ldflags="-s -w" -o "$out" ./cmd/hapidays; \
  done

FROM scratch AS export
COPY --from=build /out /
