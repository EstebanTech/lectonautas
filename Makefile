# Tareas del monorepo. Lo que es propio de un servicio vive en el Makefile de
# ese servicio (generar sus .pb.go, compilar, migrar); aquí solo va lo que
# cruza servicios.

SERVICES := user-service library-service interaction-service
USER_PROTO_DIR := backend/microservices/user-service/proto
LIBRARY_PROTO_DIR := backend/microservices/library-service/proto
INTERACTION_PROTO_DIR := backend/microservices/interaction-service/proto
GATEWAY_PROTO_DIR := gateway/proto

.PHONY: proto proto-descriptor up down logs

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

up:
	docker compose up -d --build

down:
	docker compose down

logs:
	docker compose logs -f
