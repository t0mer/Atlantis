# Stage 1: the UI. Static output, so it builds once on the native platform.
FROM --platform=$BUILDPLATFORM node:24-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
COPY internal/web/dist/placeholder.html /src/internal/web/dist/placeholder.html
RUN npm run build

# Stage 2: the binary, cross-compiled from the native platform (no QEMU).
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ENV GOTOOLCHAIN=local CGO_ENABLED=0
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/internal/web/dist ./internal/web/dist
ARG VERSION=docker
ARG TARGETOS TARGETARCH TARGETVARIANT
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH GOARM=${TARGETVARIANT#v} \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/atlantis ./cmd/atlantis
# scratch has no shell: create the data directory here, owned by the runtime user.
RUN mkdir -p /out/data

# Stage 3: runtime.
FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=build /out/atlantis /atlantis
COPY --from=build --chown=65532:65532 /out/data /data
USER 65532:65532
# The database (atlantis.db) and, without ATLANTIS_SECRET_KEY, the key file
# (secret.key) are created here: mount a volume to keep them.
WORKDIR /data
EXPOSE 8080
VOLUME ["/data"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/atlantis", "healthcheck"]
ENTRYPOINT ["/atlantis"]
CMD ["serve"]
