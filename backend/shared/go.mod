// Modulo compartido por los microservicios. Aqui solo va infraestructura sin
// dominio: lo que todos hacen igual y no dice nada sobre libros, usuarios ni
// interacciones. Si algo de aqui necesitara saber que es un libro, no es de
// aqui.
module github.com/EstebanTech/lectonautas/backend/shared

go 1.24

require (
	github.com/jackc/pgx/v5 v5.6.0
	github.com/joho/godotenv v1.5.1
	github.com/redis/go-redis/v9 v9.21.0
	google.golang.org/grpc v1.64.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20221227161230-091c0ba34f0a // indirect
	github.com/jackc/puddle/v2 v2.2.1 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	golang.org/x/crypto v0.21.0 // indirect
	golang.org/x/net v0.22.0 // indirect
	golang.org/x/sync v0.6.0 // indirect
	golang.org/x/sys v0.30.0 // indirect
	golang.org/x/text v0.14.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20240318140521-94a12d6c2237 // indirect
	google.golang.org/protobuf v1.34.2 // indirect
)
