# Multi-stage build for the vigil gateway.
#
# Stage 1 compiles a fully static binary (CGO_ENABLED=0) with -trimpath and
# stripped symbols (-s -w) for a small, reproducible artifact. Stage 2 is
# distroless static (nonroot): no shell, no package manager, minimal attack
# surface. Target final image < 20 MB.
#
# No HEALTHCHECK: distroless/static has no shell for the default exec form, and
# a Go-based health probe would bloat the image. Health is checked externally by
# the orchestrator (ECS/compose) against GET /v1/health.

# ---- build ----
FROM golang:1.27 AS build

WORKDIR /src

# Cache module downloads separately from source for faster rebuilds.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Static, trimmed, stripped build.
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags "-s -w" \
    -o /out/gateway ./cmd/gateway

# ---- runtime ----
FROM gcr.io/distroless/static-debian12:nonroot

# Non-root by default (the :nonroot tag runs as uid 65532).
COPY --from=build /out/gateway /gateway

EXPOSE 8080

ENTRYPOINT ["/gateway"]
