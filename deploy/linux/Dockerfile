FROM golang:1.23-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=linux GOARCH="$TARGETARCH" \
    go build -mod=mod -trimpath -ldflags='-s -w' -o /out/stoneage-gateway ./cmd/stoneage-gateway && \
    go build -mod=mod -trimpath -ldflags='-s -w' -o /out/stoneage-admin ./cmd/stoneage-admin && \
    go build -mod=mod -trimpath -ldflags='-s -w' -o /out/stoneage-operator ./cmd/stoneage-operator && \
    go build -mod=mod -trimpath -ldflags='-s -w' -o /out/stoneage-assets-sync ./cmd/stoneage-assets-sync && \
    go build -mod=mod -trimpath -ldflags='-s -w' -o /out/stoneage-web ./client/web

FROM alpine:3.22

RUN apk add --no-cache bash docker-cli docker-cli-compose netcat-openbsd

COPY --from=build /out/stoneage-gateway /opt/stoneage/bin/stoneage-gateway
COPY --from=build /out/stoneage-admin /opt/stoneage/bin/stoneage-admin
COPY --from=build /out/stoneage-operator /opt/stoneage/bin/stoneage-operator
COPY --from=build /out/stoneage-assets-sync /opt/stoneage/bin/stoneage-assets-sync
COPY --from=build /out/stoneage-web /opt/stoneage/bin/stoneage-web
# Public game assets are distributed separately and mounted by Compose.
COPY deploy/linux/compose/ /opt/stoneage/compose/

RUN chmod 0755 /opt/stoneage/bin/* /opt/stoneage/compose/*.sh

WORKDIR /opt/stoneage
