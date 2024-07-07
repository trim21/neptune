FROM powerman/dockerize:0.19 AS dockerize

FROM gcr.io/distroless/static-debian12
COPY --from=dockerize /usr/local/bin/dockerize /usr/local/bin/

COPY dist/tyr.exe /usr/local/bin/tyr

ENTRYPOINT ["/usr/local/bin/tyr"]

