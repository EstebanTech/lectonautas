package config

import (
	"fmt"

	"github.com/EstebanTech/lectonautas/backend/shared/config"
)

type Config struct {
	GRPCPort    string
	DatabaseURL string
	RedisAddr   string
	// LibraryAddr es library-service, al que se le pide borrar el contenido de
	// una cuenta que se da de baja.
	LibraryAddr string
	// InteractionAddr es interaction-service, al que se le pide lo mismo con lo
	// que esa cuenta dejo como lector: me gusta, comentarios y calificaciones.
	InteractionAddr string
	// InternalSecret autentica las llamadas de servicio a servicio, en los dos
	// sentidos: se manda al llamar a un vecino y se exige en ValidateSession,
	// que es el metodo interno de este servicio.
	InternalSecret string
}

// Load resuelve la configuración del servicio. Dentro de docker, el compose
// inyecta GRPC_PORT y DATABASE_URL ya resueltas; fuera de docker se usan las
// variables prefijadas del .env global de la raíz del monorepo.
func Load() (*Config, error) {
	cfg := &Config{
		GRPCPort:        config.Env("GRPC_PORT", "USER_SERVICE_GRPC_PORT"),
		DatabaseURL:     config.Env("DATABASE_URL", "USER_SERVICE_DATABASE_URL"),
		RedisAddr:       config.Env("REDIS_ADDR", "USER_SERVICE_REDIS_ADDR"),
		LibraryAddr:     config.Env("LIBRARY_SERVICE_ADDR"),
		InteractionAddr: config.Env("INTERACTION_SERVICE_ADDR"),
	}

	cfg.GRPCPort = config.FirstNonEmpty(cfg.GRPCPort, "50051")
	cfg.RedisAddr = config.FirstNonEmpty(cfg.RedisAddr, "localhost:6379")
	cfg.LibraryAddr = config.FirstNonEmpty(cfg.LibraryAddr, "library-service:50052")
	cfg.InteractionAddr = config.FirstNonEmpty(cfg.InteractionAddr, "interaction-service:50053")

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL (or USER_SERVICE_DATABASE_URL) is required")
	}

	secret, err := config.InternalSecret()
	if err != nil {
		return nil, err
	}
	cfg.InternalSecret = secret

	return cfg, nil
}
