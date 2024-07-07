FROM busybox:1-musl AS builder

FROM debian AS copier

ARG TARGETARCH

COPY dist/tyr_linux_$TARGETARCH /dist/tyr

COPY --from=builder /bin/wget /usr/local/bin/wget

FROM gcr.io/distroless/static-debian12

COPY --from=copier /dist/tyr /usr/local/bin/wget /usr/local/bin/

ENTRYPOINT ["/usr/local/bin/tyr"]
