SERVICE := interaction
ROOT_DIR := ../../..
# La URL de la BD sale del .env global de la raíz del monorepo
DB_URL ?= $(shell grep -E '^INTERACTION_SERVICE_DATABASE_URL=' $(ROOT_DIR)/.env | cut -d '=' -f2-)
PROTO_PATHS := --proto_path=proto --proto_path=proto/third_party

.PHONY: proto tidy build run test test-integration migrate

# Genera los .pb.go del contrato propio y de los stubs de cliente que este
# servicio usa (user-service para los tokens, library-service para los libros).
# El descriptor que consume el gateway NO se genera aquí: es uno solo con todos
# los servicios y lo arma el Makefile de la raíz (`make proto-descriptor`).
proto:
	protoc \
		$(PROTO_PATHS) \
		--go_out=proto --go_opt=paths=source_relative \
		--go-grpc_out=proto --go-grpc_opt=paths=source_relative \
		proto/interaction/v1/interaction.proto \
		proto/user/v1/user_auth.proto \
		proto/library/v1/library_content.proto

tidy:
	go mod tidy

# Pruebas de la lógica de negocio: no necesitan Postgres, Valkey ni los vecinos.
test:
	go test ./... -count=1

# Añade las de repositorio, que sí hablan con Postgres. Sin la variable, esas
# pruebas se saltan solas, que es lo que hace `make test`.
test-integration:
	INTERACTION_TEST_DATABASE_URL="$(DB_URL)" go test ./... -count=1

build:
	go build -o bin/interaction-service ./cmd/server

run:
	go run ./cmd/server

# Las migraciones no se aplican desde aquí: las corre el servicio *-migrate del
# compose, que además lleva el registro de lo aplicado en la tabla
# public.schema_migrations de la base. Esto solo dispara ese runner.
#
# Antes había un migrate-up/migrate-down que llamaba al binario `migrate`, que
# no está instalado en ningún lado del proyecto: eran targets muertos.
migrate:
	docker compose -f $(ROOT_DIR)/docker-compose.yml up $(SERVICE)-migrate
