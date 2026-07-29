package config

import (
	"fmt"

	"github.com/EstebanTech/lectonautas/backend/shared/config"
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
	// InternalSecret autentica las llamadas de servicio a servicio, en los dos
	// sentidos: se manda al llamar a un vecino y se exige en los metodos
	// internos de este servicio (los dos borrados masivos).
	InternalSecret string
}

// Load resuelve la configuración del servicio. Dentro de docker, el compose
// inyecta GRPC_PORT y DATABASE_URL ya resueltas; fuera de docker se usan las
// variables prefijadas del .env global de la raíz del monorepo.
func Load() (*Config, error) {
	cfg := &Config{
		GRPCPort:           config.Env("GRPC_PORT", "INTERACTION_SERVICE_GRPC_PORT"),
		DatabaseURL:        config.Env("DATABASE_URL", "INTERACTION_SERVICE_DATABASE_URL"),
		RedisAddr:          config.Env("REDIS_ADDR", "INTERACTION_SERVICE_REDIS_ADDR"),
		WSPort:             config.Env("WS_PORT", "INTERACTION_SERVICE_WS_PORT"),
		UserServiceAddr:    config.Env("USER_SERVICE_ADDR"),
		LibraryServiceAddr: config.Env("LIBRARY_SERVICE_ADDR"),
	}

	cfg.GRPCPort = config.FirstNonEmpty(cfg.GRPCPort, "50053")
	cfg.RedisAddr = config.FirstNonEmpty(cfg.RedisAddr, "localhost:6379")
	cfg.WSPort = config.FirstNonEmpty(cfg.WSPort, "8090")
	cfg.UserServiceAddr = config.FirstNonEmpty(cfg.UserServiceAddr, "localhost:50051")
	cfg.LibraryServiceAddr = config.FirstNonEmpty(cfg.LibraryServiceAddr, "localhost:50052")

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL (or INTERACTION_SERVICE_DATABASE_URL) is required")
	}

	secret, err := config.InternalSecret()
	if err != nil {
		return nil, err
	}
	cfg.InternalSecret = secret

	return cfg, nil
}
