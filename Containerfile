# syntax=docker/dockerfile:1.18

ARG GO_IMAGE=docker.io/library/golang:1.26.5-bookworm@sha256:1ecb7edf62a0408027bd5729dfd6b1b8766e578e8df93995b225dfd0944eb651
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

FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS rclone
WORKDIR /src/rclone

ARG TARGETOS
ARG TARGETARCH
ARG RCLONE_VERSION=1.74.4
ARG RCLONE_COMMIT=5bc93a2a7ab0ebd0a11352bc4968eabeffb18027
ARG GRPC_VERSION=1.82.1

RUN git init \
    && git remote add origin https://github.com/rclone/rclone.git \
    && git fetch --depth=1 origin "$RCLONE_COMMIT" \
    && git checkout --detach FETCH_HEAD \
    && test "$(git rev-parse HEAD)" = "$RCLONE_COMMIT" \
    && test "$(git describe --tags --exact-match)" = "v${RCLONE_VERSION}" \
    && go mod edit -require="google.golang.org/grpc@v${GRPC_VERSION}" \
    && go mod download \
    && go mod verify \
    && CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
       go build -trimpath -ldflags="-s -w" -o /out/rclone .

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
