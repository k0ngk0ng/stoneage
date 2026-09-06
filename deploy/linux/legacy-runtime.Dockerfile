# Build the complete legacy runtime from the checked-in 2.5 GMSV/SAAC source.
# The final image contains no live character/account records; those are mounted
# from the host by docker-compose.yml.
FROM alpine:3.22 AS build

WORKDIR /src
COPY server/legacy/source/2.5/ /src/
COPY server/legacy/modern/ /modern/
COPY scripts/modernize-character-file-read.py \
     scripts/modernize-login-announcement.py \
     scripts/modernize-nu-flow-control.py \
     scripts/modernize-quiz-state.py \
     scripts/redact-legacy-password-logs.py /

RUN sh /modern/build.sh

FROM alpine:3.22

RUN apk add --no-cache netcat-openbsd

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

RUN chmod 0755 /usr/local/bin/stoneage-runtime /opt/stoneage/bin/*.sh \
    /opt/stoneage/defaults/gmsv/gmsvjt.exe /opt/stoneage/defaults/saac/saacjt.exe

WORKDIR /game
ENTRYPOINT ["/usr/local/bin/stoneage-runtime"]
