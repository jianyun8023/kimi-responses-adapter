# Multi-stage build: compiles a static musl binary from source, so the image
# is self-contained (no prebuilt artifact needed). Works under QEMU for
# multi-arch builds; `uname -m` reports the target arch there.
FROM rust:bookworm AS build
WORKDIR /app
RUN apt-get update \
    && apt-get install -y --no-install-recommends musl-tools \
    && rm -rf /var/lib/apt/lists/*
COPY Cargo.toml Cargo.lock ./
COPY src ./src
RUN target="$(uname -m)-unknown-linux-musl" \
    && rustup target add "$target" \
    && cargo build --release --locked --target "$target" \
    && cp "target/$target/release/kimi-responses-adapter" /out-kimi-responses-adapter

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out-kimi-responses-adapter /usr/local/bin/kimi-responses-adapter
EXPOSE 8787
ENTRYPOINT ["/usr/local/bin/kimi-responses-adapter"]
