FROM golang:1.23-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
# Only Go build inputs belong in this layer. Legacy C sources, map data and
# deployment documentation must not invalidate the control-plane compilation.
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY server/go/ ./server/go/
COPY client/web/*.go client/web/index.html client/web/sw.js ./client/web/

ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=linux GOARCH="$TARGETARCH" \
    go build -mod=readonly -trimpath -ldflags='-s -w' -o /out/ \
    ./cmd/stoneage-gateway ./cmd/stoneage-admin ./cmd/stoneage-operator \
    ./cmd/stoneage-assets-sync ./client/web && \
    mv /out/web /out/stoneage-web

FROM alpine:3.22

# Use local time consistently for server announcements and runtime logs.
ENV TZ=Asia/Shanghai

RUN apk add --no-cache tzdata bash docker-cli docker-cli-compose netcat-openbsd

COPY --from=build /out/stoneage-gateway /opt/stoneage/bin/stoneage-gateway
COPY --from=build /out/stoneage-admin /opt/stoneage/bin/stoneage-admin
COPY --from=build /out/stoneage-operator /opt/stoneage/bin/stoneage-operator
COPY --from=build /out/stoneage-assets-sync /opt/stoneage/bin/stoneage-assets-sync
COPY --from=build /out/stoneage-web /opt/stoneage/bin/stoneage-web
# Public game assets are distributed separately and mounted by Compose.
COPY deploy/linux/compose/ /opt/stoneage/compose/

RUN chmod 0755 /opt/stoneage/bin/* /opt/stoneage/compose/*.sh

WORKDIR /opt/stoneage
