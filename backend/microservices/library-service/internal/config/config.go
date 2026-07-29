package config

import (
	"fmt"

	"github.com/EstebanTech/lectonautas/backend/shared/config"
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
	// InternalSecret autentica las llamadas de servicio a servicio, en los dos
	// sentidos: se manda al llamar a un vecino y se exige en los metodos
	// internos de este servicio.
	InternalSecret string
}

// Load resuelve la configuración del servicio. Dentro de docker, el compose
// inyecta GRPC_PORT y DATABASE_URL ya resueltas; fuera de docker se usan las
// variables prefijadas del .env global de la raíz del monorepo.
func Load() (*Config, error) {
	cfg := &Config{
		GRPCPort:               config.Env("GRPC_PORT", "LIBRARY_SERVICE_GRPC_PORT"),
		DatabaseURL:            config.Env("DATABASE_URL", "LIBRARY_SERVICE_DATABASE_URL"),
		RedisAddr:              config.Env("REDIS_ADDR", "LIBRARY_SERVICE_REDIS_ADDR"),
		UserServiceAddr:        config.Env("USER_SERVICE_ADDR"),
		InteractionServiceAddr: config.Env("INTERACTION_SERVICE_ADDR"),
	}

	cfg.GRPCPort = config.FirstNonEmpty(cfg.GRPCPort, "50052")
	cfg.RedisAddr = config.FirstNonEmpty(cfg.RedisAddr, "localhost:6379")
	cfg.UserServiceAddr = config.FirstNonEmpty(cfg.UserServiceAddr, "localhost:50051")
	cfg.InteractionServiceAddr = config.FirstNonEmpty(cfg.InteractionServiceAddr, "localhost:50053")

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL (or LIBRARY_SERVICE_DATABASE_URL) is required")
	}

	secret, err := config.InternalSecret()
	if err != nil {
		return nil, err
	}
	cfg.InternalSecret = secret

	return cfg, nil
}
