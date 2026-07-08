FROM busybox:1-musl@sha256:8635836765b0c4c43970660219739baa58b0883c2e429e4b8918f7dd1519455c AS builder

ARG TARGETARCH

COPY dist/neptune_linux_$TARGETARCH /dist/neptune

FROM gcr.io/distroless/static-debian13@sha256:3592aa8171c77482f62bbc4164e6a2d141c6122554ace66e5cc910cadb961ff0

COPY --from=builder /bin/wget /usr/local/bin/wget
COPY --from=builder /dist/neptune /usr/local/bin/neptune

ENTRYPOINT ["/usr/local/bin/neptune"]
