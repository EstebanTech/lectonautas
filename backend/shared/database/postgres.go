// Package database abre el pool de Postgres. Es idéntico en todos los
// servicios: cada uno apunta a su propia base ("database per service"), pero la
// forma de conectarse no tiene por qué variar.
package database

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPostgresPool abre el pool y comprueba que la base responda antes de
// devolverlo: es mejor que el servicio no arranque a que arranque y falle en la
// primera petición.
func NewPostgresPool(databaseURL string) (*pgxpool.Pool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("unable to create connection pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("unable to ping database: %w", err)
	}

	return pool, nil
}
