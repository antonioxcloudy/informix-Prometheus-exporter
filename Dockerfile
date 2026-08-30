# syntax=docker/dockerfile:1

FROM golang:1.27-bookworm AS build

RUN apt-get update \
    && apt-get install -y --no-install-recommends unixodbc-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
RUN CGO_ENABLED=1 go build \
    -trimpath \
    -ldflags "-s -w \
      -X github.com/anmartsan/prometheus-informix-exporter/internal/version.Version=${VERSION} \
      -X github.com/anmartsan/prometheus-informix-exporter/internal/version.Commit=${COMMIT} \
      -X github.com/anmartsan/prometheus-informix-exporter/internal/version.BuildDate=${BUILD_DATE}" \
    -o /out/informix_exporter ./cmd/informix_exporter

FROM debian:bookworm-slim AS csdk-init

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates gzip \
    && rm -rf /var/lib/apt/lists/*

COPY --chmod=755 deploy/csdk-init.sh /usr/local/bin/csdk-init

ENTRYPOINT ["/usr/local/bin/csdk-init"]

FROM debian:bookworm-slim AS runtime

ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown

LABEL org.opencontainers.image.title="Prometheus Informix Exporter" \
      org.opencontainers.image.description="Prometheus exporter for IBM/HCL Informix over ODBC" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${BUILD_DATE}" \
      org.opencontainers.image.source="https://github.com/anmartsan/prometheus-informix-exporter" \
      org.opencontainers.image.licenses="GPL-3.0-only"

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        libaio1 \
        libcrypt1 \
        unixodbc \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --system --gid 65532 exporter \
    && useradd --system --uid 65532 --gid 65532 --home-dir /nonexistent --shell /usr/sbin/nologin exporter \
    && mkdir -p /etc/informix-exporter \
    && chown exporter:exporter /etc/informix-exporter

COPY --from=build /out/informix_exporter /usr/local/bin/informix_exporter
COPY --chmod=755 deploy/docker-entrypoint.sh /usr/local/bin/docker-entrypoint

ENV INFORMIXDIR=/opt/informix \
    ODBCINI=/etc/informix-exporter/odbc.ini \
    LD_LIBRARY_PATH=/opt/informix/lib:/opt/informix/lib/esql:/opt/informix/lib/cli

USER 65532:65532

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
    CMD ["curl", "--fail", "--silent", "http://127.0.0.1:8080/healthz"]

ENTRYPOINT ["/usr/local/bin/docker-entrypoint"]
CMD ["--configfile=/etc/informix-exporter/config.yaml", "--port=8080"]
