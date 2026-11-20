# syntax=docker/dockerfile:1.7

FROM golang:1.26.11-alpine3.24@sha256:ae52b5bc6ed045a9ed26ab03dc19327a802a9cc9aa8b27ed57f5c6b60579b0bb AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/pxgo .

FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf AS runtime

RUN apk add --no-cache \
      ca-certificates=20261020-r0 \
      krb5=1.22.3-r0 \
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
