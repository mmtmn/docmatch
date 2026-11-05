# docmatch API, worker and migrator (services/api) in one small image: the three static binaries
# on Alpine, running as an unprivileged user. Built from the repository root (context ../..);
# api.Dockerfile.dockerignore keeps the context to what the build reads.
#
#   docker build -f deploy/local/api.Dockerfile -t docmatch-api .
#
# The same uid (10001) runs the harness image: the harness socket is 0600 and the media folder is
# shared, so API, worker and harness must be the same user.
ARG GO_IMAGE=golang:1.27.3-alpine
ARG RUNTIME_IMAGE=alpine:3.22

FROM ${GO_IMAGE} AS build
ENV CGO_ENABLED=0 GOTOOLCHAIN=local GOFLAGS=-mod=mod
WORKDIR /src
COPY packages/contracts/gen packages/contracts/gen
COPY services/api/go.mod services/api/go.sum services/api/
RUN --mount=type=cache,id=docmatch-gomod,target=/go/pkg/mod \
    cd services/api && go mod download
COPY services/api services/api
ARG VERSION=dev
ARG REVISION=unknown
RUN --mount=type=cache,id=docmatch-gomod,target=/go/pkg/mod \
    --mount=type=cache,id=docmatch-gobuild,target=/root/.cache/go-build \
    cd services/api && \
    pkg=github.com/mmtmn/docmatch/services/api/internal/version && \
    go build -trimpath \
      -ldflags "-s -w -X ${pkg}.Version=${VERSION} -X ${pkg}.GitSHA=${REVISION} -X ${pkg}.BuiltAt=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
      -o /out/ ./cmd/api ./cmd/worker ./cmd/migrate

FROM ${RUNTIME_IMAGE}
ARG VERSION=dev
ARG REVISION=unknown
LABEL org.opencontainers.image.title="docmatch-api" \
      org.opencontainers.image.description="docmatch: API, worker e migrações (modo local)" \
      org.opencontainers.image.licenses="AGPL-3.0-only" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}"
RUN addgroup -S -g 10001 docmatch && adduser -S -D -H -u 10001 -G docmatch docmatch && \
    mkdir -p /app/config /data/media /run/docmatch && chown docmatch:docmatch /data/media /run/docmatch
COPY --from=build /out/api /usr/local/bin/docmatch-api
COPY --from=build /out/worker /usr/local/bin/docmatch-worker
COPY --from=build /out/migrate /usr/local/bin/docmatch-migrate
# --chmod: checkouts with a strict umask leave the files unreadable to the runtime user.
COPY --chmod=0644 config/models.yaml config/pricing.yaml /app/config/
WORKDIR /app
USER 10001:10001
ENV DM_PRICING_FILE=/app/config/pricing.yaml DM_MODELS_FILE=/app/config/models.yaml
EXPOSE 8080
CMD ["docmatch-api"]
