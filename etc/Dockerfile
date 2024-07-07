FROM busybox:1 AS builder

FROM gcr.io/distroless/static-debian12

COPY --from=builder /bin/wget /usr/bin/wget

COPY dist/tyr.exe /usr/local/bin/tyr

ENTRYPOINT ["/usr/local/bin/tyr"]

