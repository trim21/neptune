FROM busybox:1-musl AS builder

ARG TARGETARCH

COPY dist/tyr_linux_$TARGETARCH /dist/tyr

FROM gcr.io/distroless/static-debian12

COPY --from=builder /bin/wget /usr/local/bin/wget
COPY --from=builder /dist/tyr /usr/local/bin/tyr

ENTRYPOINT ["/usr/local/bin/tyr"]
