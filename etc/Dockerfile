FROM busybox AS builder

FROM gcr.io/distroless/static-debian12

COPY --from=builder /wget /usr/bin/wget

COPY dist/tyr.exe /usr/local/bin/tyr

ENTRYPOINT ["/usr/local/bin/tyr"]

