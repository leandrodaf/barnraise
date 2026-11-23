# syntax=docker/dockerfile:1

# ---- build ----
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/barnraise ./cmd/barnraise

# ---- runtime ----
FROM debian:stable-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends git ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --uid 10001 --user-group --create-home --home-dir /home/barnraise --shell /usr/sbin/nologin barnraise \
 && mkdir -p /data/share /data/config \
 && chown -R barnraise:barnraise /data \
 && chmod 0700 /data/share /data/config
COPY --from=build /out/barnraise /usr/local/bin/barnraise

ENV BARNRAISE_DATA_DIR=/data/share \
    BARNRAISE_CONFIG_DIR=/data/config \
    DISABLE_AUTOUPDATER=1 \
    BARNRAISE_NO_SELF_UPDATE=1
# Containers upgrade by pulling a new image, so the cluster self-update is off.
VOLUME ["/data"]
# 7420: the API (only reachable from outside with --listen 0.0.0.0:7420 plus
#       --tls-cert/--tls-key; the daemon refuses non-loopback without TLS).
# 4222: embedded NATS, for the other machines of the cluster.
EXPOSE 7420 4222
USER barnraise
WORKDIR /home/barnraise

# Default: the API on the unix socket /data/share/barnraise.sock and on
# 127.0.0.1:7420 inside the container. To expose it, override the command:
#   docker run … barnraise daemon --listen 0.0.0.0:7420 --tls-cert /tls/tls.crt --tls-key /tls/tls.key
ENTRYPOINT ["barnraise"]
CMD ["daemon"]
