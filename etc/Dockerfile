FROM busybox:1-musl AS builder

FROM gcr.io/distroless/static-debian12

COPY --from=builder /bin/wget /usr/bin/wget

COPY dist/tyr_linux_amd64 /usr/local/bin/tyr

ENTRYPOINT ["/usr/local/bin/tyr"]

