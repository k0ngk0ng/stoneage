# Compile on the runner's native architecture and cross-compile the binaries
# for the requested image platform. This keeps the arm64 Go compiler out of
# QEMU while preserving the target architecture in the final image.
FROM --platform=$BUILDPLATFORM golang:1.23-alpine AS go-source

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
# Only Go build inputs belong in this layer. Legacy C sources, map data and
# deployment documentation must not invalidate the control-plane compilation.
COPY client/web/runtimeassets/ ./client/web/runtimeassets/
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY server/go/ ./server/go/

ARG TARGETOS
ARG TARGETARCH

FROM go-source AS build-control

RUN CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    go build -mod=readonly -trimpath -ldflags="-s -w" -o /out/ \
    ./cmd/stoneage-gateway ./cmd/stoneage-admin ./cmd/stoneage-operator \
    ./cmd/stoneage-assets-sync

COPY client/web/*.go ./client/web/

ARG RELEASE_VERSION=dev
RUN CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    go build -mod=readonly -trimpath -ldflags="-s -w -X main.releaseVersion=$RELEASE_VERSION" \
    -o /out/web ./client/web && \
    mv /out/web /out/stoneage-web

FROM alpine:3.22 AS control-plane

# Use local time consistently for server announcements and runtime logs.
ENV TZ=Asia/Shanghai

RUN apk add --no-cache tzdata bash docker-cli docker-cli-compose netcat-openbsd

COPY --from=build-control /out/stoneage-gateway /opt/stoneage/bin/stoneage-gateway
COPY --from=build-control /out/stoneage-admin /opt/stoneage/bin/stoneage-admin
COPY --from=build-control /out/stoneage-operator /opt/stoneage/bin/stoneage-operator
COPY --from=build-control /out/stoneage-assets-sync /opt/stoneage/bin/stoneage-assets-sync
COPY --from=build-control /out/stoneage-web /opt/stoneage/bin/stoneage-web
COPY ai/catalogs/ /opt/stoneage/ai/catalogs/
# Public game assets are distributed separately and mounted by Compose.
COPY deploy/linux/compose/ /opt/stoneage/compose/

RUN chmod 0755 /opt/stoneage/bin/* /opt/stoneage/compose/*.sh


WORKDIR /opt/stoneage
