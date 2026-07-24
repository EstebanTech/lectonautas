# Lectonautas

Monorepo de Lectonautas. El backend son microservicios en Go que se comunican
entre sí por **gRPC**, detrás de un **API Gateway (Envoy)** que expone HTTP/JSON
al cliente y lo traduce a gRPC.

## Tecnologías

- **Go 1.24** — microservicios
- **gRPC** + **Protocol Buffers** — comunicación entre servicios
- **Envoy** — API Gateway (transcoding HTTP↔gRPC, CORS, rate limiting)
- **PostgreSQL 16** — persistencia
- **Valkey** (Redis) — rate limiting y cache de sesiones (tokens con TTL de 2h)
- **Docker Compose** — entorno local

## Estructura

```
lectonautas/
├── backend/microservices/users-service/   # Servicio de usuarios (Go + gRPC)
├── backend/monoliths/                       # Vacío
├── frontend/                                # Vacío
├── gateway/                                 # Config de Envoy y rate limiting
└── docker-compose.yml
```

## Requisitos

- **Docker Desktop** (o Docker Engine + Compose). Nada más: Go, PostgreSQL, Valkey
  y Envoy corren dentro de contenedores; no hace falta instalarlos en la máquina.

## Levantar el entorno

```powershell
Copy-Item .env.example .env      # solo la primera vez (Linux/Mac: cp .env.example .env)
docker compose up -d --build
```

Eso es todo. `.env.example` trae valores por defecto que funcionan en local, y las
migraciones de la base de datos **se aplican solas** al arrancar (servicio
`users-migrate`), así que no hay pasos manuales. Cuando los contenedores estén
arriba, la API queda en `http://localhost:8080`.

Servicios y puertos:

- Gateway (HTTP): `:8080`
- Admin de Envoy: `:9901`
- PostgreSQL: `:5433`
- Valkey: `:6379`

## API HTTP (users-service)

| Método | Ruta | Acción |
|---|---|---|
| `POST` | `/v1/users` | Crear usuario |
| `GET` | `/v1/users/{id}` | Obtener usuario |
| `PATCH` | `/v1/users/{id}` | Actualizar usuario |
| `DELETE` | `/v1/users/{id}` | Eliminar usuario |
| `GET` | `/v1/users` | Listar usuarios |
| `POST` | `/v1/auth/login` | Login: devuelve un token de sesión |
| `GET` | `/v1/auth/me` | Usuario del token (header `Authorization: Bearer`) |
| `POST` | `/v1/auth/logout` | Cierra la sesión del token |

En la raíz hay una colección de Postman (`lectonautas.postman_collection.json`)
con todos estos endpoints.

### Autenticación

`login` genera un token aleatorio, lo devuelve **en crudo** al cliente (única
vez) y guarda solo su **hash** (SHA-256) en la BD (`session`) y en Valkey con
TTL de 2h. En cada request autenticado el token del header se hashea y se busca
primero en Valkey; si no está, se valida contra la BD y se repuebla Valkey con
el tiempo restante (cache-aside).

## Estado

- `users-service`: CRUD de usuarios + login/sesiones (tabla `session` + Valkey).
  Pendiente: tests.
- Gateway probado end-to-end (transcoding, CORS, rate limiting a 80 req/min por IP).
- `frontend/` y `backend/monoliths/` aún sin código.
