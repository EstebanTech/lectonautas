package service

import (
	"context"
	"log"
	"sync"

	"github.com/EstebanTech/lectonautas/backend/microservices/library-service/internal/auth"
	"github.com/EstebanTech/lectonautas/backend/microservices/library-service/internal/cache"
	"github.com/EstebanTech/lectonautas/backend/microservices/library-service/internal/interaction"
	"github.com/EstebanTech/lectonautas/backend/microservices/library-service/internal/repository"
	libraryv1 "github.com/EstebanTech/lectonautas/backend/microservices/library-service/proto/library/v1"
)

// Authenticator resuelve la identidad del llamante. Es una interfaz y no el
// tipo concreto de auth para que las reglas de visibilidad y propiedad —
// lo mas delicado de este servicio — se puedan probar sin levantar
// user-service.
type Authenticator interface {
	// Require exige un token valido y devuelve el user_id.
	Require(ctx context.Context) (string, error)
	// Optional devuelve cadena vacia si no vino token, y error si vino uno
	// invalido.
	Optional(ctx context.Context) (string, error)
}

// Cache es lo que el servicio necesita de Valkey. Igual que Authenticator, se
// abstrae para poder probar sin dependencias externas. El servicio no lo usa
// directo: lo envuelve cacheAside, que le pone la politica de fallos.
type Cache interface {
	Key(ctx context.Context, parts ...string) (string, error)
	Get(ctx context.Context, key string, dest any) error
	Set(ctx context.Context, key string, value any) error
	Bump(ctx context.Context) error
}

// Interactions borra en interaction-service lo que colgaba de un libro que
// aqui desaparece: sus me gusta, sus comentarios y sus calificaciones. Viven en
// otra base, asi que el CASCADE que se lleva capitulos y vinculos de saga no
// los alcanza.
type Interactions interface {
	// Tiene que ser idempotente: se llama despues de borrar el libro, y si
	// falla, un reintento posterior vuelve a pasar por aqui.
	DeleteBookInteractions(ctx context.Context, bookID string) error
}

// Las implementaciones reales tienen que seguir cumpliendo el contrato.
var (
	_ Authenticator = (*auth.Authenticator)(nil)
	_ Cache         = (*cache.LibraryCache)(nil)
	_ Interactions  = (*interaction.Client)(nil)
)

// LibraryService implementa la API del servicio sobre un repositorio por
// entidad. Lo que el lector hace con los libros (guardarlos, marcarlos como
// favoritos) no vive aqui: es otro dominio y va en su propio servicio.
//
// Los handlers estan repartidos por entidad (book.go, chapter.go, saga.go,
// genre.go, account.go); aqui queda el armado. Lo que comparten esta en sus
// propios colaboradores: cacheAside para el cache, access.go para las reglas de
// propiedad y visibilidad, validation.go para los campos.
type LibraryService struct {
	libraryv1.UnimplementedLibraryServiceServer
	books        repository.BookRepository
	chapters     repository.ChapterRepository
	sagas        repository.SagaRepository
	genres       repository.GenreRepository
	cache        cacheAside
	auth         Authenticator
	interactions Interactions
}

func NewLibraryService(
	books repository.BookRepository,
	chapters repository.ChapterRepository,
	sagas repository.SagaRepository,
	genres repository.GenreRepository,
	libraryCache Cache,
	authenticator Authenticator,
	interactions Interactions,
) *LibraryService {
	return &LibraryService{
		books:        books,
		chapters:     chapters,
		sagas:        sagas,
		genres:       genres,
		cache:        cacheAside{cache: libraryCache},
		auth:         authenticator,
		interactions: interactions,
	}
}

// dropInteractionsWorkers acota cuantas limpiezas se piden a la vez. El tope
// existe por el vecino, no por nosotros: la baja de un autor con cien libros no
// puede convertirse en cien llamadas simultaneas contra interaction-service.
const dropInteractionsWorkers = 8

// dropInteractions le pide a interaction-service que limpie lo que colgaba de
// los libros que se acaban de borrar.
//
// Va DESPUES del borrado y es best-effort: si se hiciera antes y el borrado
// fallara, se habrian perdido los comentarios de un libro que sigue vivo, que
// es peor. Al ir despues, lo que puede quedar son filas huerfanas apuntando a
// un libro que ya no existe: invisibles para el lector (no hay libro que
// abrir) y recuperables con otra pasada. Por eso solo se registra el fallo, en
// vez de hacer fallar un borrado que ya ocurrio y no tiene vuelta atras.
//
// Las llamadas van en paralelo con un tope: la baja de cuenta pasa por aqui con
// todos los libros del autor de golpe, y hacerlas en fila encadenaba un
// round-trip por libro mientras el cliente espera la respuesta.
func (s *LibraryService) dropInteractions(ctx context.Context, bookIDs ...string) {
	if len(bookIDs) == 0 {
		return
	}
	if len(bookIDs) == 1 {
		// El caso normal (borrar un libro): no hay nada que paralelizar y no
		// vale la pena montar goroutines para una sola llamada.
		s.dropBookInteractions(ctx, bookIDs[0])
		return
	}

	sem := make(chan struct{}, dropInteractionsWorkers)
	var wg sync.WaitGroup
	for _, id := range bookIDs {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			s.dropBookInteractions(ctx, id)
		}()
	}
	wg.Wait()
}

func (s *LibraryService) dropBookInteractions(ctx context.Context, bookID string) {
	if err := s.interactions.DeleteBookInteractions(ctx, bookID); err != nil {
		log.Printf("delete book interactions failed (book %s): %v", bookID, err)
	}
}
