FROM busybox:1-musl@sha256:8635836765b0c4c43970660219739baa58b0883c2e429e4b8918f7dd1519455c AS builder

ARG TARGETARCH

COPY dist/neptune_linux_$TARGETARCH /dist/neptune

FROM gcr.io/distroless/static-debian12@sha256:20bc6c0bc4d625a22a8fde3e55f6515709b32055ef8fb9cfbddaa06d1760f838

COPY --from=builder /bin/wget /usr/local/bin/wget
COPY --from=builder /dist/neptune /usr/local/bin/neptune

ENTRYPOINT ["/usr/local/bin/neptune"]
