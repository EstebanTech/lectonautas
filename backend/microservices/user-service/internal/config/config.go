package config

import (
	"fmt"
	"os"
)

type Config struct {
	GRPCPort    string
	DatabaseURL string
	RedisAddr   string
}

// Load resuelve la configuración del servicio. Dentro de docker, el compose
// inyecta GRPC_PORT y DATABASE_URL ya resueltas; fuera de docker se usan las
// variables prefijadas del .env global de la raíz del monorepo.
func Load() (*Config, error) {
	cfg := &Config{
		GRPCPort:    firstNonEmpty(os.Getenv("GRPC_PORT"), os.Getenv("USER_SERVICE_GRPC_PORT"), "50051"),
		DatabaseURL: firstNonEmpty(os.Getenv("DATABASE_URL"), os.Getenv("USER_SERVICE_DATABASE_URL")),
		RedisAddr:   firstNonEmpty(os.Getenv("REDIS_ADDR"), os.Getenv("USER_SERVICE_REDIS_ADDR"), "localhost:6379"),
	}

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL (or USER_SERVICE_DATABASE_URL) is required")
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
