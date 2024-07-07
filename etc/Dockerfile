FROM busybox:1-musl AS builder

FROM debian AS copier

COPY dist/ /dist/

COPY etc/keep-arch-file.bash /keep-arch-file.bash

RUN bash /keep-arch-file.bash

COPY --from=builder /bin/wget /usr/local/bin/wget

FROM gcr.io/distroless/static-debian12

COPY --from=copier /dist/tyr /usr/local/bin/wget /usr/local/bin/

ENTRYPOINT ["/usr/local/bin/tyr"]
