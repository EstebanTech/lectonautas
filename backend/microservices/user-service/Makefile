ROOT_DIR := ../../..
# La URL de la BD sale del .env global de la raíz del monorepo
DB_URL ?= $(shell grep -E '^USER_SERVICE_DATABASE_URL=' $(ROOT_DIR)/.env | cut -d '=' -f2-)
PROTO_PATHS := --proto_path=proto --proto_path=proto/third_party

.PHONY: proto tidy build run test test-integration migrate-up migrate-down

# Genera los .pb.go del contrato. El descriptor que consume el gateway NO se
# genera aquí: es uno solo con todos los servicios y lo arma el Makefile de la
# raíz (`make proto-descriptor`).
proto:
	protoc \
		$(PROTO_PATHS) \
		--go_out=proto --go_opt=paths=source_relative \
		--go-grpc_out=proto --go-grpc_opt=paths=source_relative \
		proto/user/v1/user.proto

tidy:
	go mod tidy

# Pruebas de la lógica de negocio: no necesitan Postgres ni Valkey.
test:
	go test ./... -count=1

# Añade las de repositorio, que sí hablan con Postgres. Sin la variable, esas
# pruebas se saltan solas, que es lo que hace `make test`.
test-integration:
	USER_TEST_DATABASE_URL="$(DB_URL)" go test ./... -count=1

build:
	go build -o bin/user-service ./cmd/server

run:
	go run ./cmd/server

migrate-up:
	migrate -path migrations -database "$(DB_URL)" up

migrate-down:
	migrate -path migrations -database "$(DB_URL)" down
