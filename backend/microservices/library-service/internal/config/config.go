package config

import (
	"fmt"
	"os"
)

type Config struct {
	GRPCPort    string
	DatabaseURL string
	RedisAddr   string
	// Direccion gRPC de user-service, el unico dueno de las sesiones: este
	// servicio le pregunta por cada token (rpc ValidateSession).
	UserServiceAddr string
	// Direccion gRPC de interaction-service, al que se le pide limpiar los me
	// gusta, comentarios y calificaciones de un libro que se borra aqui.
	InteractionServiceAddr string
}

// Load resuelve la configuración del servicio. Dentro de docker, el compose
// inyecta GRPC_PORT y DATABASE_URL ya resueltas; fuera de docker se usan las
// variables prefijadas del .env global de la raíz del monorepo.
func Load() (*Config, error) {
	cfg := &Config{
		GRPCPort:        firstNonEmpty(os.Getenv("GRPC_PORT"), os.Getenv("LIBRARY_SERVICE_GRPC_PORT"), "50052"),
		DatabaseURL:     firstNonEmpty(os.Getenv("DATABASE_URL"), os.Getenv("LIBRARY_SERVICE_DATABASE_URL")),
		RedisAddr:       firstNonEmpty(os.Getenv("REDIS_ADDR"), os.Getenv("LIBRARY_SERVICE_REDIS_ADDR"), "localhost:6379"),
		UserServiceAddr: firstNonEmpty(os.Getenv("USER_SERVICE_ADDR"), "localhost:50051"),
		InteractionServiceAddr: firstNonEmpty(os.Getenv("INTERACTION_SERVICE_ADDR"),
			"localhost:50053"),
	}

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL (or LIBRARY_SERVICE_DATABASE_URL) is required")
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
