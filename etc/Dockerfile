FROM busybox:1-musl AS builder

FROM debian:slim AS copier

COPY dist/ /dist/
#COPY dist/tyr_linux_arm64 /usr/local/bin/tyr-arm64

COPY etc/keep-arch-file.bash /keep-arch-file.bash

RUN bash /keep-arch-file.bash

FROM gcr.io/distroless/static-debian12

COPY --from=builder /bin/wget /usr/bin/wget

COPY --from=copier /dist/tyr /usr/local/bin/tyr

ENTRYPOINT ["/usr/local/bin/tyr"]
