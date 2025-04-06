FROM busybox:1-musl@sha256:5b2531a497e1f4f0994272e72168d765cc54dc5da760e7bbac9c95aec383b137 AS builder

ARG TARGETARCH

COPY dist/neptune_linux_$TARGETARCH /dist/neptune

FROM gcr.io/distroless/static-debian12@sha256:3d0f463de06b7ddff27684ec3bfd0b54a425149d0f8685308b1fdf297b0265e9

COPY --from=builder /bin/wget /usr/local/bin/wget
COPY --from=builder /dist/neptune /usr/local/bin/neptune

ENTRYPOINT ["/usr/local/bin/neptune"]
