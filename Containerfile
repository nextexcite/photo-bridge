# syntax=docker/dockerfile:1.18

ARG GO_IMAGE=docker.io/library/golang:1.26.5-bookworm@sha256:1ecb7edf62a0408027bd5729dfd6b1b8766e578e8df93995b225dfd0944eb651
ARG FETCH_IMAGE=docker.io/library/debian:13.1-slim@sha256:a347fd7510ee31a84387619a492ad6c8eb0af2f2682b916ff3e643eb076f925a
ARG RUNTIME_IMAGE=gcr.io/distroless/static-debian13:nonroot@sha256:f7f8f729987ad0fdf6b05eeeae94b26e6a0f613bdf46feea7fc40f7bd72953e6

FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS build
WORKDIR /src

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG VCS_REF=unknown
ARG BUILD_DATE=unknown

COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath \
      -ldflags="-s -w \
        -X github.com/nextexcite/photo-bridge/internal/buildinfo.Version=${VERSION} \
        -X github.com/nextexcite/photo-bridge/internal/buildinfo.Commit=${VCS_REF} \
        -X github.com/nextexcite/photo-bridge/internal/buildinfo.Date=${BUILD_DATE}" \
      -o /out/photo-bridge ./cmd/photo-bridge

FROM --platform=$BUILDPLATFORM ${FETCH_IMAGE} AS rclone
ARG TARGETARCH
ARG RCLONE_VERSION=1.74.4
ARG RCLONE_SHA256_AMD64=fe435e0c36228e7c2f116a8701f01127bb1f694005fc11d1f27186c8bca4115d
ARG RCLONE_SHA256_ARM64=97685285c9ad6a0cf17d5844115d2a67245af6444db672187074bd9c358de419

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl unzip \
    && rm -rf /var/lib/apt/lists/* \
    && case "$TARGETARCH" in \
         amd64) expected="$RCLONE_SHA256_AMD64" ;; \
         arm64) expected="$RCLONE_SHA256_ARM64" ;; \
         *) echo "unsupported architecture: $TARGETARCH" >&2; exit 1 ;; \
       esac \
    && archive="rclone-v${RCLONE_VERSION}-linux-${TARGETARCH}.zip" \
    && curl --fail --location --silent --show-error \
         "https://downloads.rclone.org/v${RCLONE_VERSION}/${archive}" \
         --output "/tmp/${archive}" \
    && echo "${expected}  /tmp/${archive}" | sha256sum --check --strict \
    && unzip -q "/tmp/${archive}" -d /tmp/rclone \
    && install -D -m 0755 "/tmp/rclone/rclone-v${RCLONE_VERSION}-linux-${TARGETARCH}/rclone" /out/rclone

FROM ${RUNTIME_IMAGE}

ARG VERSION=dev
ARG VCS_REF=unknown
ARG BUILD_DATE=unknown

LABEL org.opencontainers.image.title="photo-bridge" \
      org.opencontainers.image.description="Non-destructive, manifest-backed archival copies through rclone" \
      org.opencontainers.image.source="https://github.com/nextexcite/photo-bridge" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.version="$VERSION" \
      org.opencontainers.image.revision="$VCS_REF" \
      org.opencontainers.image.created="$BUILD_DATE"

COPY --from=build /out/photo-bridge /usr/local/bin/photo-bridge
COPY --from=rclone /out/rclone /usr/local/bin/rclone

ENV PHOTOBRIDGE_CONFIG=/config/config.yaml \
    PHOTOBRIDGE_STATE_DIR=/state \
    PHOTOBRIDGE_RCLONE_BIN=/usr/local/bin/rclone

USER 65532:65532
ENTRYPOINT ["/usr/local/bin/photo-bridge"]
CMD ["help"]
