# syntax=docker/dockerfile:1.7

FROM golang:1.25.13-alpine3.24@sha256:1e0126852075c9c60731c8ba49088448b91f63e2aed97ca9d1a9791622a05946 AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/pxgo .

FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6 AS runtime

RUN apk add --no-cache \
      ca-certificates=20260909-r0 \
      krb5=1.22.2-r1 \
      tini=0.19.0-r3 && \
    addgroup -S -g 65532 pxgo && \
    adduser -S -D -H -u 65532 -G pxgo -h /home/pxgo -s /sbin/nologin pxgo && \
    install -d -o 65532 -g 65532 -m 0700 /home/pxgo /home/pxgo/.config

ENV HOME=/home/pxgo \
    XDG_CONFIG_HOME=/home/pxgo/.config \
    TMPDIR=/tmp

WORKDIR /pxgo
COPY --from=builder --chmod=0555 /out/pxgo /usr/local/bin/pxgo
COPY --chmod=0444 pxgo.ini /pxgo/pxgo.ini
COPY --chmod=0555 docker/start.sh /pxgo/start.sh

USER 65532:65532

EXPOSE 3128
STOPSIGNAL SIGTERM
ENTRYPOINT ["/sbin/tini", "--", "/bin/sh", "/pxgo/start.sh"]
