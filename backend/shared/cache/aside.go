package cache

import (
	"context"
	"errors"
	"log/slog"

	"github.com/EstebanTech/lectonautas/backend/shared/logx"
)

// Store es lo que Aside necesita de un cache. Lo cumple Versioned, y en las
// pruebas lo cumple un doble en memoria: es lo que permite afirmar que una
// lectura cacheada de verdad no toca la base de datos.
type Store interface {
	Key(ctx context.Context, parts ...string) (string, error)
	Get(ctx context.Context, key string, dest any) error
	Set(ctx context.Context, key string, value any) error
	Bump(ctx context.Context) error
}

// Aside encapsula el protocolo de cache-aside y, sobre todo, la política de
// fallos que lo acompaña: nada de lo que le pase a Valkey puede tumbar una
// petición. Un miss, un timeout, un contador de versión ilegible y un JSON de
// un formato viejo son para el llamante la misma cosa —"no estaba, sigue contra
// la BD"—, y lo único que los distingue es que el fallo de verdad se deja en el
// log.
//
// Es un objeto y no tres métodos sueltos del servicio porque esa política es
// una responsabilidad entera y separable: el servicio decide QUÉ cachear, y
// esto decide CÓMO se comporta el cache cuando falla. Estaba copiado tal cual
// en los tres servicios.
type Aside struct {
	store Store
}

func NewAside(store Store) Aside {
	return Aside{store: store}
}

// Get intenta servir desde Valkey. Devuelve la clave —para que el llamante
// pueda repoblarla después sin recalcularla— y si hubo acierto. La clave sale
// vacía cuando no se pudo ni construir, y en ese caso Set la ignora.
func (a Aside) Get(ctx context.Context, dest any, parts ...string) (string, bool) {
	key, err := a.store.Key(ctx, parts...)
	if err != nil {
		logx.From(ctx).Warn("cache key failed", slog.String("error", err.Error()))
		return "", false
	}

	if err := a.store.Get(ctx, key, dest); err != nil {
		if !errors.Is(err, ErrMiss) {
			logx.From(ctx).Warn("cache get failed", slog.String("error", err.Error()))
		}
		return key, false
	}
	return key, true
}

// Set repuebla la clave. Un fallo aquí solo cuesta un miss más adelante.
func (a Aside) Set(ctx context.Context, key string, value any) {
	if key == "" {
		return
	}
	if err := a.store.Set(ctx, key, value); err != nil {
		logx.From(ctx).Warn("cache set failed", slog.String("error", err.Error()))
	}
}

// Invalidate corre después de cada escritura. No es fatal si falla: el TTL
// acaba tapando el hueco, pero se deja en el log porque hasta entonces se
// estarían sirviendo datos viejos.
func (a Aside) Invalidate(ctx context.Context) {
	if err := a.store.Bump(ctx); err != nil {
		logx.From(ctx).Error("cache invalidate failed", slog.String("error", err.Error()))
	}
}
