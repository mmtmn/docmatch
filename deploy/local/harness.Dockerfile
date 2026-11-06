# docmatch harness (services/harness, dm-server) as a static musl binary on Alpine. Like
# `make -C services/harness release`, the binary is linked by the rust-lld that ships with Rust (no
# cross toolchain: musl-tools compiles the TLS library's native code). Built natively for the machine's
# architecture; the cargo registry and target dir live in BuildKit cache mounts, so a rebuild only
# compiles what changed. Built from the repository root (context ../..).
#
#   docker build -f deploy/local/harness.Dockerfile -t docmatch-harness .
ARG RUST_IMAGE=rust:1.93.0-slim-trixie
ARG RUNTIME_IMAGE=alpine:3.22

FROM ${RUST_IMAGE} AS build
ENV CARGO_TERM_COLOR=never \
    CARGO_TARGET_X86_64_UNKNOWN_LINUX_MUSL_LINKER=rust-lld \
    CARGO_TARGET_AARCH64_UNKNOWN_LINUX_MUSL_LINKER=rust-lld \
    CARGO_PROFILE_RELEASE_STRIP=symbols
RUN rustup target add "$(uname -m)-unknown-linux-musl"
RUN apt-get update && apt-get install -y --no-install-recommends musl-tools && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY packages/contracts/harness.proto packages/contracts/
COPY packages/contracts/gen/limits.rs packages/contracts/gen/
COPY services/harness services/harness
# rust-toolchain.toml would make rustup fetch clippy and rustfmt; the image already has 1.93.0.
RUN --mount=type=cache,id=docmatch-cargo-registry,target=/usr/local/cargo/registry \
    --mount=type=cache,id=docmatch-cargo-target,target=/cache/target \
    rm -f services/harness/rust-toolchain.toml && \
    target="$(uname -m)-unknown-linux-musl" && \
    cd services/harness && \
    CARGO_TARGET_DIR=/cache/target cargo build --release --locked -p dm-server --target "$target" && \
    mkdir -p /out && cp "/cache/target/$target/release/dm-server" /out/dm-server

FROM ${RUNTIME_IMAGE}
ARG VERSION=dev
ARG REVISION=unknown
LABEL org.opencontainers.image.title="docmatch-harness" \
      org.opencontainers.image.description="docmatch: harness de conciliação (dm-server, modo local)" \
      org.opencontainers.image.licenses="AGPL-3.0-only" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}"
RUN addgroup -S -g 10001 docmatch && adduser -S -D -H -u 10001 -G docmatch docmatch && \
    mkdir -p /data/media /run/docmatch /etc/docmatch && chmod 0755 /etc/docmatch && \
    chown docmatch:docmatch /data/media /run/docmatch
COPY --from=build /out/dm-server /usr/local/bin/dm-server
COPY --chmod=0644 config/models.yaml /etc/docmatch/models.yaml
USER 10001:10001
ENV DM_HARNESS_SOCKET=/run/docmatch/harness.sock DM_MEDIA_LOCAL_DIR=/data/media DM_MODELS_FILE=/etc/docmatch/models.yaml
CMD ["dm-server"]
