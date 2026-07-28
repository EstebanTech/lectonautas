package service

import (
	"context"
	"errors"
	"log"

	"github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/internal/cache"
)

// cacheAside encapsula el protocolo de cache-aside y, sobre todo, la politica de
// fallos que lo acompana: aqui nada de lo que le pase a Valkey puede tumbar una
// peticion. Un miss, un timeout, un contador de version ilegible y un JSON de un
// formato viejo son para el llamante la misma cosa —"no estaba, sigue contra la
// BD"—, y lo unico que los distingue es que el fallo de verdad se deja en el log.
//
// Es un objeto y no tres metodos sueltos del servicio porque esa politica es una
// responsabilidad entera y separable: el servicio decide QUE cachear, y esto
// decide COMO se comporta el cache cuando falla.
type cacheAside struct {
	cache Cache
}

// Get intenta servir desde Valkey. Devuelve la clave —para que el llamante
// pueda repoblarla despues sin recalcularla— y si hubo acierto. La clave sale
// vacia cuando no se pudo ni construir, y en ese caso Set la ignora.
func (c cacheAside) Get(ctx context.Context, dest any, parts ...string) (string, bool) {
	key, err := c.cache.Key(ctx, parts...)
	if err != nil {
		log.Printf("cache key failed: %v", err)
		return "", false
	}

	if err := c.cache.Get(ctx, key, dest); err != nil {
		if !errors.Is(err, cache.ErrMiss) {
			log.Printf("cache get failed: %v", err)
		}
		return key, false
	}
	return key, true
}

// Set repuebla la clave. Un fallo aqui solo cuesta un miss mas adelante.
func (c cacheAside) Set(ctx context.Context, key string, value any) {
	if key == "" {
		return
	}
	if err := c.cache.Set(ctx, key, value); err != nil {
		log.Printf("cache set failed: %v", err)
	}
}

// Invalidate corre despues de cada escritura. No es fatal si falla: el TTL acaba
// tapando el hueco, pero se deja en el log porque hasta entonces se estarian
// sirviendo contadores viejos.
func (c cacheAside) Invalidate(ctx context.Context) {
	if err := c.cache.Bump(ctx); err != nil {
		log.Printf("cache invalidate failed: %v", err)
	}
}
