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
    go build -mod=mod -trimpath -ldflags='-s -w' -o /out/stoneage-web ./client/web

FROM alpine:3.22

RUN apk add --no-cache bash docker-cli docker-cli-compose netcat-openbsd

COPY --from=build /out/stoneage-gateway /opt/stoneage/bin/stoneage-gateway
COPY --from=build /out/stoneage-admin /opt/stoneage/bin/stoneage-admin
COPY --from=build /out/stoneage-operator /opt/stoneage/bin/stoneage-operator
COPY --from=build /out/stoneage-web /opt/stoneage/bin/stoneage-web
# The generated sprite pack is immutable release content. Maps, WAVs and NPC
# definitions stay on the host because they come from the matching 2.5 client
# and server runtime and can be updated without rebuilding the control image.
COPY client/web/assets/original/ /opt/stoneage/web-assets/
COPY deploy/linux/compose/ /opt/stoneage/compose/

RUN chmod 0755 /opt/stoneage/bin/* /opt/stoneage/compose/*.sh

WORKDIR /opt/stoneage
