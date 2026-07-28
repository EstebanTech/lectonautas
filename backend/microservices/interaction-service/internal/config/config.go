package config

import (
	"fmt"
	"os"
)

type Config struct {
	GRPCPort    string
	DatabaseURL string
	RedisAddr   string
	// WSPort es el puerto del servidor HTTP del WebSocket, aparte del de gRPC.
	// Son dos protocolos que no se pueden multiplexar en el mismo listener: el
	// transcoder del gateway no habla WebSocket, asi que esa ruta llega como
	// HTTP/1.1 crudo y necesita su propio puerto.
	WSPort string
	// Direccion gRPC de user-service, el unico dueno de las sesiones: este
	// servicio le pregunta por cada token (rpc ValidateSession).
	UserServiceAddr string
	// Direccion gRPC de library-service, dueno de los libros: se le pregunta si
	// el libro existe y esta publicado antes de aceptar una interaccion.
	LibraryServiceAddr string
}

// Load resuelve la configuración del servicio. Dentro de docker, el compose
// inyecta GRPC_PORT y DATABASE_URL ya resueltas; fuera de docker se usan las
// variables prefijadas del .env global de la raíz del monorepo.
func Load() (*Config, error) {
	cfg := &Config{
		GRPCPort:           firstNonEmpty(os.Getenv("GRPC_PORT"), os.Getenv("INTERACTION_SERVICE_GRPC_PORT"), "50053"),
		DatabaseURL:        firstNonEmpty(os.Getenv("DATABASE_URL"), os.Getenv("INTERACTION_SERVICE_DATABASE_URL")),
		RedisAddr:          firstNonEmpty(os.Getenv("REDIS_ADDR"), os.Getenv("INTERACTION_SERVICE_REDIS_ADDR"), "localhost:6379"),
		WSPort:             firstNonEmpty(os.Getenv("WS_PORT"), os.Getenv("INTERACTION_SERVICE_WS_PORT"), "8090"),
		UserServiceAddr:    firstNonEmpty(os.Getenv("USER_SERVICE_ADDR"), "localhost:50051"),
		LibraryServiceAddr: firstNonEmpty(os.Getenv("LIBRARY_SERVICE_ADDR"), "localhost:50052"),
	}

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL (or INTERACTION_SERVICE_DATABASE_URL) is required")
	}

	return cfg, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
