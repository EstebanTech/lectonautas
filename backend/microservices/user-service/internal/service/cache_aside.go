package service

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/EstebanTech/lectonautas/backend/microservices/user-service/internal/cache"
	"github.com/EstebanTech/lectonautas/backend/microservices/user-service/internal/domain"
)

// userCacheTTL es cuanto viven en Valkey los datos de usuario que devuelven los
// GET. Es corto a proposito: las escrituras que pasan por la API invalidan la
// clave al instante, pero este TTL es el techo de cuanto puede quedar viejo un
// dato que cambio por fuera (escritura directa a la BD, otro servicio).
const userCacheTTL = 15 * time.Minute

// cachedUsers envuelve al UserCache con la politica de fallos del servicio, que
// antes estaba repartida por los handlers: aqui nada de lo que le pase a Valkey
// puede tumbar una peticion. Un miss, un timeout y un JSON de un formato viejo
// son para el llamante la misma cosa —"no estaba"—, y lo unico que los
// distingue es que el error de verdad se deja en el log.
//
// Al concentrarlo aqui, los handlers dejan de repetir el par
// `errors.Is(err, cache.ErrMiss)` + `log.Printf` y pasan a leerse como lo que
// son: pedir el dato, y si no esta, ir a buscarlo.
type cachedUsers struct {
	cache UserCache
}

// user devuelve el usuario cacheado y si de verdad hubo acierto. El bool y no un
// error, porque para el llamante no hay nada que decidir: si no hay acierto, va
// a la BD y punto.
func (c cachedUsers) user(ctx context.Context, id string) (*domain.User, bool) {
	u, err := c.cache.GetUser(ctx, id)
	if err != nil {
		logUnlessMiss("user cache get failed", err)
		return nil, false
	}
	return u, true
}

// storeUser repuebla la clave del usuario. Un fallo aqui solo cuesta un miss mas
// adelante: el dato ya se resolvio contra la BD.
func (c cachedUsers) storeUser(ctx context.Context, u *domain.User) {
	if err := c.cache.SetUser(ctx, u, userCacheTTL); err != nil {
		log.Printf("user cache set failed: %v", err)
	}
}

func (c cachedUsers) all(ctx context.Context) ([]*domain.User, bool) {
	users, err := c.cache.GetAllUsers(ctx)
	if err != nil {
		logUnlessMiss("all users cache get failed", err)
		return nil, false
	}
	return users, true
}

func (c cachedUsers) storeAll(ctx context.Context, users []*domain.User) {
	if err := c.cache.SetAllUsers(ctx, users, userCacheTTL); err != nil {
		log.Printf("all users cache set failed: %v", err)
	}
}

// forgetUser corre despues de cada escritura sobre el usuario. Se invalida en
// vez de reescribir la clave: si el Set fallara, quedaria sirviendo el usuario
// viejo hasta que venza el TTL.
func (c cachedUsers) forgetUser(ctx context.Context, id string) {
	if err := c.cache.InvalidateUser(ctx, id); err != nil {
		log.Printf("user cache invalidate failed: %v", err)
	}
}

// forgetAll tira el listado completo, que queda viejo en cuanto se da de alta o
// de baja a cualquiera.
func (c cachedUsers) forgetAll(ctx context.Context) {
	if err := c.cache.InvalidateAllUsers(ctx); err != nil {
		log.Printf("all users cache invalidate failed: %v", err)
	}
}

// logUnlessMiss deja en el log solo los fallos reales de Valkey. Un miss es el
// funcionamiento normal del cache y no dice nada; anotarlo llenaria el log de
// ruido y taparia justo lo que hay que ver.
func logUnlessMiss(what string, err error) {
	if !errors.Is(err, cache.ErrMiss) {
		log.Printf("%s: %v", what, err)
	}
}
