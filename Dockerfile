# syntax=docker/dockerfile:1

# Cross-compile instead of emulating: with buildx the builder runs on
# $BUILDPLATFORM (the runner's native arch) and cargo targets the musl triple
# for $TARGETPLATFORM, so linux/arm64 no longer needs slow QEMU emulation.
FROM --platform=$BUILDPLATFORM rust:bookworm AS build
ARG TARGETPLATFORM
WORKDIR /app
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        musl-tools gcc-aarch64-linux-gnu gcc-x86-64-linux-gnu \
        libc6-dev-arm64-cross libc6-dev-amd64-cross \
    && rm -rf /var/lib/apt/lists/*
# ring (rustls) compiles C via cc-rs, and rust's self-contained musl linking
# uses `cc` as the linker driver; both must point at the cross gcc matching
# the target arch.
ENV CC_x86_64_unknown_linux_musl=x86_64-linux-gnu-gcc \
    CC_aarch64_unknown_linux_musl=aarch64-linux-gnu-gcc \
    CARGO_TARGET_X86_64_UNKNOWN_LINUX_MUSL_LINKER=x86_64-linux-gnu-gcc \
    CARGO_TARGET_AARCH64_UNKNOWN_LINUX_MUSL_LINKER=aarch64-linux-gnu-gcc

# Dependencies are fetched in a layer that only depends on the lockfile, so
# source-only changes reuse it (exported via the GHA build cache).
COPY Cargo.toml Cargo.lock ./
# Dummy main so `cargo fetch` accepts the manifest before real sources land.
RUN mkdir src && echo 'fn main() {}' > src/main.rs && cargo fetch --locked

COPY src ./src
# Incremental compile artifacts live in a BuildKit cache mount, outside the
# image layers. The target triple subdir keeps amd64/arm64 artifacts apart.
RUN --mount=type=cache,target=/app/target,id=cargo-target \
    case "$TARGETPLATFORM" in \
        "linux/amd64") target=x86_64-unknown-linux-musl ;; \
        "linux/arm64") target=aarch64-unknown-linux-musl ;; \
        *) echo "unsupported platform: $TARGETPLATFORM" >&2; exit 1 ;; \
    esac \
    && rustup target add "$target" \
    && cargo build --release --locked --offline --target "$target" \
    && cp "target/$target/release/kimi-responses-adapter" /out-kimi-responses-adapter

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out-kimi-responses-adapter /usr/local/bin/kimi-responses-adapter
EXPOSE 8787
ENTRYPOINT ["/usr/local/bin/kimi-responses-adapter"]
