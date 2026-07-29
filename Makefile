# Tareas del monorepo. Lo que es propio de un servicio vive en el Makefile de
# ese servicio (generar sus .pb.go, compilar, migrar); aquí solo va lo que
# cruza servicios.

SERVICES := user-service library-service interaction-service
# Los módulos Go del repo: los tres servicios más la infraestructura común.
# Son módulos distintos, así que build/vet/test se ejecutan en cada uno.
MODULES := backend/shared $(addprefix backend/microservices/,$(SERVICES))

USER_PROTO_DIR := backend/microservices/user-service/proto
LIBRARY_PROTO_DIR := backend/microservices/library-service/proto
INTERACTION_PROTO_DIR := backend/microservices/interaction-service/proto
GATEWAY_PROTO_DIR := gateway/proto

.PHONY: proto proto-descriptor check-descriptor build vet test check smoke up down logs

# Regenera los stubs de todos los servicios.
proto:
	@for s in $(SERVICES); do \
		echo "==> $$s"; \
		$(MAKE) -C backend/microservices/$$s proto || exit 1; \
	done

# Genera el FileDescriptorSet que consume el filtro grpc_json_transcoder de
# Envoy. Es UNO SOLO con todos los servicios porque el filtro admite un único
# proto_descriptor: cada servicio nuevo se agrega a esta lista y a `services`
# en gateway/envoy.yaml. Tras regenerarlo hay que reiniciar el gateway para que
# lo recargue (`docker compose restart gateway`).
#
# third_party se toma del user-service y sirve para todos: google/api/*.proto
# es idéntico en cada servicio, y pasar los dos árboles haría que protoc viera
# el mismo archivo dos veces.
proto-descriptor:
	protoc \
		--proto_path=$(USER_PROTO_DIR) \
		--proto_path=$(LIBRARY_PROTO_DIR) \
		--proto_path=$(INTERACTION_PROTO_DIR) \
		--proto_path=$(USER_PROTO_DIR)/third_party \
		--include_imports --include_source_info \
		--descriptor_set_out=$(GATEWAY_PROTO_DIR)/services.pb \
		user/v1/user.proto library/v1/library.proto interaction/v1/interaction.proto

# Comprueba que el descriptor de arriba está al día con los .proto. No hace
# falta protoc: mira que cada rpc, mensaje y campo esté dentro del descriptor.
check-descriptor:
	@bash scripts/check-descriptor.sh

build:
	@for m in $(MODULES); do \
		echo "==> $$m"; \
		(cd $$m && go build ./...) || exit 1; \
	done

vet:
	@for m in $(MODULES); do \
		echo "==> $$m"; \
		(cd $$m && go vet ./...) || exit 1; \
	done

# Las pruebas que no necesitan nada levantado. Las de repositorio se saltan
# solas si no hay base configurada; para incluirlas está el `test-integration`
# del Makefile de cada servicio.
test:
	@for m in $(MODULES); do \
		echo "==> $$m"; \
		(cd $$m && go test ./... -count=1) || exit 1; \
	done

# Todo lo que el CI exige antes de mezclar.
check: build vet test check-descriptor

# Prueba de humo contra el entorno ya levantado: recorre el camino real de una
# petición cruzando los tres servicios y el gateway.
smoke:
	@bash scripts/smoke-test.sh

up:
	docker compose up -d --build --wait

down:
	docker compose down

logs:
	docker compose logs -f
