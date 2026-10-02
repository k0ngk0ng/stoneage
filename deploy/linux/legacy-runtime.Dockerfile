# Build the complete legacy runtime from the checked-in 2.5 GMSV/SAAC source.
# The final image contains no live character/account records; those are mounted
# from the host by docker-compose.yml.
FROM alpine:3.22 AS build

# GCC expands historical __DATE__/__TIME__ from this common source timestamp.
# Separate server/learner builds must not differ solely by wall-clock time.
ARG SOURCE_DATE_EPOCH=0
ENV SOURCE_DATE_EPOCH=$SOURCE_DATE_EPOCH

WORKDIR /src
COPY server/legacy/source/2.5/ /src/
COPY server/legacy/modern/ /modern/
COPY scripts/modernize-character-file-read.py \
     scripts/modernize-login-announcement.py \
     scripts/modernize-nu-flow-control.py \
     scripts/modernize-quiz-state.py \
     scripts/modernize-object-cstring.py \
     scripts/redact-legacy-password-logs.py /

RUN sh /modern/build.sh
COPY config/gmsv/setup.cf.example /battle-smoke.cf
COPY bin/battle-export.py /battle-export.py
# Test the actual compiled engine before an image can be published.
RUN sh /modern/tests/run-battle-dataset-smoke.sh
RUN sh /modern/tests/run-ladder-native-smoke.sh
RUN sh /modern/tests/run-battle-parity.sh /src/gmsv /battle-smoke.cf
RUN sh /modern/tests/run-battle-parity.sh /src/gmsv /battle-smoke.cf guardian

FROM alpine:3.22 AS legacy-runtime
LABEL org.stoneage.character-format="pet-items-v1"
ARG RELEASE_VERSION=dev
ENV STONEAGE_RELEASE_VERSION=$RELEASE_VERSION

# Use local time consistently for server announcements and runtime logs.
ENV TZ=Asia/Shanghai

RUN apk add --no-cache tzdata netcat-openbsd sqlite-libs

COPY --from=build /src/gmsv/gmsvjt.exe /opt/stoneage/defaults/gmsv/gmsvjt.exe
COPY --from=build /src/gmsv/data /opt/stoneage/defaults/gmsv/data
COPY --from=build /src/gmsv/Dengon /opt/stoneage/defaults/gmsv/Dengon
COPY --from=build /src/gmsv/Schedule /opt/stoneage/defaults/gmsv/Schedule
COPY config/gmsv/setup.cf.example /opt/stoneage/defaults/gmsv/setup.cf
COPY --from=build /src/gmsv/badpetstring.txt /opt/stoneage/defaults/gmsv/badpetstring.txt
COPY --from=build /src/gmsv/log.cf /opt/stoneage/defaults/gmsv/log.cf
COPY --from=build /src/saac/saacjt.exe /opt/stoneage/defaults/saac/saacjt.exe
COPY config/saac/acserv.cf.example /opt/stoneage/defaults/saac/acserv.cf
COPY --from=build /src/saac/badpetstring.txt /opt/stoneage/defaults/saac/badpetstring.txt
COPY server/legacy/modern/runtime-entrypoint.sh /usr/local/bin/stoneage-runtime
COPY server/legacy/modern/run-gmsv.sh /opt/stoneage/bin/run-gmsv.sh
COPY server/legacy/modern/run-saac.sh /opt/stoneage/bin/run-saac.sh
COPY server/legacy/modern/prepare-battle-rules.sh /opt/stoneage/bin/prepare-battle-rules.sh
COPY server/legacy/modern/run-battle-dataset.sh /opt/stoneage/bin/run-battle-dataset.sh
COPY server/legacy/modern/run-battle-environment.sh /opt/stoneage/bin/run-battle-environment.sh

RUN chmod 0755 /usr/local/bin/stoneage-runtime /opt/stoneage/bin/*.sh \
    /opt/stoneage/defaults/gmsv/gmsvjt.exe /opt/stoneage/defaults/saac/saacjt.exe

WORKDIR /game
ENTRYPOINT ["/usr/local/bin/stoneage-runtime"]

# A separate local learner image. It reuses the exact native build above;
# no game server entrypoint or live account/character data is included.
FROM --platform=$BUILDPLATFORM golang:1.23-alpine AS training-client
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/sactl/ ./cmd/sactl/
COPY internal/ ./internal/
COPY server/go/ ./server/go/
ARG TARGETOS
ARG TARGETARCH
ARG RELEASE_VERSION=dev
RUN CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    go build -mod=readonly -trimpath -ldflags="-s -w -X main.version=$RELEASE_VERSION" \
    -o /out/sactl ./cmd/sactl

FROM alpine:3.22 AS ai-training
LABEL org.stoneage.training.schema="1"
LABEL org.opencontainers.image.source="https://github.com/k0ngk0ng/stoneage"
ARG RELEASE_VERSION=dev
LABEL org.opencontainers.image.version=$RELEASE_VERSION
RUN apk add --no-cache sqlite-libs
COPY --from=build /src/gmsv/gmsvjt.exe /opt/stoneage/defaults/gmsv/gmsvjt.exe
COPY --from=build /src/gmsv/data /opt/stoneage/defaults/gmsv/data
COPY config/gmsv/setup.cf.example /opt/stoneage/defaults/gmsv/setup.cf
COPY --from=training-client /out/sactl /opt/stoneage/bin/sactl
COPY server/legacy/modern/prepare-battle-rules.sh \
     server/legacy/modern/run-battle-environment.sh \
     server/legacy/modern/start-training-worker.sh /opt/stoneage/bin/
COPY config/arena-agent/training-environment.json /opt/stoneage/training-environment.json
COPY .agents/skills/sactl/ /opt/stoneage/skills/sactl/
COPY docs/learned-*.md /opt/stoneage/docs/
ENV STONEAGE_TRAINING_ENVIRONMENT=/opt/stoneage/training-environment.json \
    STONEAGE_BATTLE_RECORD_DIR=/data/rules
RUN chmod 0755 /opt/stoneage/bin/*.sh /opt/stoneage/bin/sactl \
    /opt/stoneage/defaults/gmsv/gmsvjt.exe
WORKDIR /data
# Build-time smoke performs actual collection and training, with no login.
RUN /opt/stoneage/bin/sactl ai environment check && \
    /opt/stoneage/bin/sactl ai train --data-dir /tmp/training-smoke --pet-skills 1,2,20 \
      --warmup-matches 0 --batch-matches 2 --batches 1 --max-turns 40 && \
    rm -rf /tmp/training-smoke /data/rules
ENTRYPOINT ["/opt/stoneage/bin/sactl", "ai"]
STOPSIGNAL SIGINT
CMD ["--help"]
