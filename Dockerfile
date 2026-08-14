# Goreleaser builds the binary before invoking this Dockerfile (see
# .goreleaser.yaml `dockers_v2`); only the prebuilt artifact is copied in.
FROM gcr.io/distroless/static-debian12:nonroot
ARG TARGETPLATFORM
COPY $TARGETPLATFORM/kimi-responses-adapter /usr/local/bin/kimi-responses-adapter
EXPOSE 8787
ENTRYPOINT ["/usr/local/bin/kimi-responses-adapter"]
