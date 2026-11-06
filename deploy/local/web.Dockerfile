# docmatch site (apps/web) as a static build served by dm-web (tools/installer/cmd/dm-web), a tiny
# Go server with the production URL rules. The site is compiled once with placeholder origins and
# dm-web swaps them for DM_PUBLIC_BASE_URL and DM_API_BASE_URL at start, so the same image works on
# any port. Built from the repository root (context ../..).
#
#   docker build -f deploy/local/web.Dockerfile -t docmatch-web .
ARG NODE_IMAGE=node:22.23.4-trixie-slim
ARG RUST_IMAGE=rust:1.93.0-slim-trixie
ARG GO_IMAGE=golang:1.27.3-alpine
ARG RUNTIME_IMAGE=alpine:3.22

FROM ${RUST_IMAGE} AS books
RUN --mount=type=cache,id=docmatch-mdbook-registry,target=/usr/local/cargo/registry \
    --mount=type=cache,id=docmatch-mdbook-target,target=/cache/target \
    CARGO_TARGET_DIR=/cache/target cargo install mdbook --version 0.5.4 --locked --root /out

FROM ${NODE_IMAGE} AS site
ENV CI=1 ASTRO_TELEMETRY_DISABLED=1 COREPACK_ENABLE_DOWNLOAD_PROMPT=0 PNPM_HOME=/pnpm PATH=/pnpm:$PATH
RUN corepack enable && corepack prepare pnpm@11.5.3 --activate
COPY --from=books /out/bin/mdbook /usr/local/bin/mdbook
WORKDIR /src
COPY package.json pnpm-workspace.yaml pnpm-lock.yaml .npmrc ./
COPY apps/web/package.json apps/web/
COPY packages/locales/package.json packages/locales/
COPY packages/design-tokens/package.json packages/design-tokens/
COPY sdks/typescript/package.json sdks/typescript/
RUN --mount=type=cache,id=docmatch-pnpm-store,target=/pnpm/store \
    pnpm install --frozen-lockfile --store-dir /pnpm/store --filter "@docmatch/web..."
COPY packages/locales packages/locales
COPY packages/design-tokens packages/design-tokens
COPY packages/contracts/limits.yaml packages/contracts/openapi.yaml packages/contracts/
COPY config/pricing.yaml config/
COPY docs/book docs/book
COPY sdks/python/examples/project.py sdks/python/examples/project.py
COPY sdks/go/examples/project/main.go sdks/go/examples/project/main.go
COPY sdks/rust/examples/project.rs sdks/rust/examples/project.rs
COPY sdks/typescript/examples/project.mjs sdks/typescript/examples/project.mjs
COPY apps/web apps/web
RUN cd apps/web && node tools/i18n-check.mjs && \
    DM_PUBLIC_BASE_URL=http://dm-site.invalid PUBLIC_DM_API_BASE_URL=http://dm-api.invalid \
    PUBLIC_DM_FEATURE_RESULTS=false pnpm exec astro build && \
    chmod -R a+rX dist

FROM ${GO_IMAGE} AS server
ENV CGO_ENABLED=0 GOTOOLCHAIN=local GOFLAGS=-mod=mod
WORKDIR /src/tools/installer
COPY sdks/go /src/sdks/go
COPY tools/installer/go.mod tools/installer/go.sum ./
COPY tools/installer/internal/webserve internal/webserve
COPY tools/installer/cmd/dm-web cmd/dm-web
RUN --mount=type=cache,id=docmatch-gomod,target=/go/pkg/mod \
    --mount=type=cache,id=docmatch-gobuild,target=/root/.cache/go-build \
    go build -trimpath -ldflags "-s -w" -o /out/dm-web ./cmd/dm-web

FROM ${RUNTIME_IMAGE}
ARG VERSION=dev
ARG REVISION=unknown
LABEL org.opencontainers.image.title="docmatch-web" \
      org.opencontainers.image.description="docmatch: site e app web (modo local)" \
      org.opencontainers.image.licenses="AGPL-3.0-only" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}"
RUN addgroup -S -g 10001 docmatch && adduser -S -D -H -u 10001 -G docmatch docmatch
COPY --from=site /src/apps/web/dist /srv/www
COPY --from=server /out/dm-web /usr/local/bin/dm-web
USER 10001:10001
ENV DM_WEB_ROOT=/srv/www DM_WEB_LISTEN=0.0.0.0:4321
EXPOSE 4321
CMD ["dm-web"]
