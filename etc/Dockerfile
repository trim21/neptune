FROM busybox:1-musl@sha256:5b2531a497e1f4f0994272e72168d765cc54dc5da760e7bbac9c95aec383b137 AS builder

ARG TARGETARCH

COPY dist/neptune_linux_$TARGETARCH /dist/neptune

FROM gcr.io/distroless/static-debian12@sha256:3f2b64ef97bd285e36132c684e6b2ae8f2723293d09aae046196cca64251acac

COPY --from=builder /bin/wget /usr/local/bin/wget
COPY --from=builder /dist/neptune /usr/local/bin/neptune

ENTRYPOINT ["/usr/local/bin/neptune"]
