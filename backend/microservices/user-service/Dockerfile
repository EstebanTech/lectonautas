# syntax=docker/dockerfile:1

# El contexto de build es backend/, no la carpeta del servicio: el go.mod de
# aqui apunta al modulo compartido con un replace relativo (../../shared) y sin
# ese modulo dentro del contexto no habria forma de resolverlo.
FROM golang:1.24-alpine AS builder

WORKDIR /src

# Primero solo los go.mod/go.sum de los dos modulos: mientras no cambien, la
# capa de descarga de dependencias se reutiliza aunque cambie el codigo.
COPY shared/go.mod shared/go.sum shared/
COPY microservices/user-service/go.mod microservices/user-service/go.sum microservices/user-service/

WORKDIR /src/microservices/user-service
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

WORKDIR /src
COPY shared/ shared/
COPY microservices/user-service/ microservices/user-service/

WORKDIR /src/microservices/user-service
# -trimpath: build reproducible sin rutas locales; -s -w: quita simbolos/debug
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build \
      -trimpath -ldflags="-s -w" \
      -o /out/user-service ./cmd/server

# La sonda del healthcheck sale del modulo compartido, no de una imagen ajena:
# es el mismo grpc que ya se compila aqui y evita depender de otro registro.
WORKDIR /src/shared
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build \
      -trimpath -ldflags="-s -w" \
      -o /out/healthprobe ./cmd/healthprobe

# distroless/static:nonroot ya trae ca-certificates, tzdata, /tmp y un usuario
# nonroot (uid 65532). Sin shell ni gestor de paquetes: superficie minima.
# El binario es 100% estatico (CGO_ENABLED=0), asi que no necesita libc.
FROM gcr.io/distroless/static:nonroot

COPY --from=builder /out/user-service /user-service

# El healthcheck del compose habla el protocolo de health de gRPC, y esta imagen
# no tiene shell ni curl con que hacerlo: necesita un binario. Es lo que permite
# que el compose sepa que el servicio RESPONDE, y no solo que el contenedor
# arranco.
COPY --from=builder /out/healthprobe /healthprobe

EXPOSE 50051
USER nonroot:nonroot
ENTRYPOINT ["/user-service"]
