ROOT_DIR := ../../..
# La URL de la BD sale del .env global de la raíz del monorepo
DB_URL ?= $(shell grep -E '^USER_SERVICE_DATABASE_URL=' $(ROOT_DIR)/.env | cut -d '=' -f2-)
PROTO_PATHS := --proto_path=proto --proto_path=proto/third_party

.PHONY: proto tidy build run migrate-up migrate-down

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

build:
	go build -o bin/user-service ./cmd/server

run:
	go run ./cmd/server

migrate-up:
	migrate -path migrations -database "$(DB_URL)" up

migrate-down:
	migrate -path migrations -database "$(DB_URL)" down
