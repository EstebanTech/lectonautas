# syntax=docker/dockerfile:1

FROM golang:1.24-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .
# -trimpath: build reproducible sin rutas locales; -s -w: quita símbolos/debug
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build \
      -trimpath -ldflags="-s -w" \
      -o /out/user-service ./cmd/server

# distroless/static:nonroot ya trae ca-certificates, tzdata, /tmp y un usuario
# nonroot (uid 65532). Sin shell ni gestor de paquetes: superficie mínima.
# El binario es 100% estático (CGO_ENABLED=0), así que no necesita libc.
FROM gcr.io/distroless/static:nonroot

COPY --from=builder /out/user-service /user-service

EXPOSE 50051
USER nonroot:nonroot
ENTRYPOINT ["/user-service"]
