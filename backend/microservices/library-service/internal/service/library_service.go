package service

import (
	"context"
	"errors"
	"log"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/estebandeveloper20/lectonautas/backend/microservices/library-service/internal/auth"
	"github.com/estebandeveloper20/lectonautas/backend/microservices/library-service/internal/cache"
	"github.com/estebandeveloper20/lectonautas/backend/microservices/library-service/internal/domain"
	"github.com/estebandeveloper20/lectonautas/backend/microservices/library-service/internal/repository"
	libraryv1 "github.com/estebandeveloper20/lectonautas/backend/microservices/library-service/proto/library/v1"
)

// Limites de longitud, alineados con los tipos de las migraciones.
const (
	titleMaxLen       = 255
	descriptionMaxLen = 5000
	coverURLMaxLen    = 500
	contentMaxLen     = 500000

	defaultPageSize = 20
	maxPageSize     = 100

	// Titulo del capitulo inicial cuando CreateBook no trae uno.
	defaultFirstChapterTitle = "Chapter 1"
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
// abstrae para poder probar sin dependencias externas.
type Cache interface {
	Key(ctx context.Context, parts ...string) (string, error)
	Get(ctx context.Context, key string, dest any) error
	Set(ctx context.Context, key string, value any) error
	Bump(ctx context.Context) error
}

// Las implementaciones reales tienen que seguir cumpliendo el contrato.
var (
	_ Authenticator = (*auth.Authenticator)(nil)
	_ Cache         = (*cache.LibraryCache)(nil)
)

// LibraryService implementa los dos modulos del servicio (content y reader).
// Se mantienen separados los repositorios de cada uno: reader nunca toca las
// tablas de content por su cuenta, siempre pasa por el repositorio de libros.
type LibraryService struct {
	libraryv1.UnimplementedLibraryServiceServer
	books    repository.BookRepository
	chapters repository.ChapterRepository
	sagas    repository.SagaRepository
	saved    repository.SavedBookRepository
	cache    Cache
	auth     Authenticator
}

func NewLibraryService(
	books repository.BookRepository,
	chapters repository.ChapterRepository,
	sagas repository.SagaRepository,
	saved repository.SavedBookRepository,
	libraryCache Cache,
	authenticator Authenticator,
) *LibraryService {
	return &LibraryService{
		books:    books,
		chapters: chapters,
		sagas:    sagas,
		saved:    saved,
		cache:    libraryCache,
		auth:     authenticator,
	}
}

// --- Cache ------------------------------------------------------------------

// cacheGet intenta servir desde Valkey. Devuelve false ante cualquier problema
// (miss, Valkey caido, JSON viejo): el llamante sigue contra la BD.
func (s *LibraryService) cacheGet(ctx context.Context, dest any, parts ...string) (string, bool) {
	key, err := s.cache.Key(ctx, parts...)
	if err != nil {
		log.Printf("cache key failed: %v", err)
		return "", false
	}

	if err := s.cache.Get(ctx, key, dest); err != nil {
		if !errors.Is(err, cache.ErrMiss) {
			log.Printf("cache get failed: %v", err)
		}
		return key, false
	}
	return key, true
}

// cacheSet repuebla la clave. Un fallo aqui solo cuesta un miss mas adelante.
func (s *LibraryService) cacheSet(ctx context.Context, key string, value any) {
	if key == "" {
		return
	}
	if err := s.cache.Set(ctx, key, value); err != nil {
		log.Printf("cache set failed: %v", err)
	}
}

// invalidate corre despues de cada escritura. No es fatal si falla: el TTL
// acaba tapando el hueco, pero se deja en el log porque hasta entonces se
// estarian sirviendo datos viejos.
func (s *LibraryService) invalidate(ctx context.Context) {
	if err := s.cache.Bump(ctx); err != nil {
		log.Printf("cache invalidate failed: %v", err)
	}
}

// --- Autorizacion -----------------------------------------------------------

// ownedBook trae el libro y verifica que el llamante sea su autor. Es el
// chequeo de propiedad que precede a toda escritura sobre libros y capitulos.
func (s *LibraryService) ownedBook(ctx context.Context, bookID, callerID string) (*domain.Book, error) {
	book, err := s.books.GetByID(ctx, bookID)
	if err != nil {
		return nil, mapRepoErr(err, "failed to load book")
	}
	if book.AuthorID != callerID {
		return nil, status.Error(codes.PermissionDenied, "only the author can modify this book")
	}
	return book, nil
}

// ownedSaga hace lo mismo para las sagas.
func (s *LibraryService) ownedSaga(ctx context.Context, sagaID, callerID string) (*domain.Saga, error) {
	saga, err := s.sagas.GetByID(ctx, sagaID)
	if err != nil {
		return nil, mapRepoErr(err, "failed to load saga")
	}
	if saga.AuthorID != callerID {
		return nil, status.Error(codes.PermissionDenied, "only the author can modify this saga")
	}
	return saga, nil
}

// visibleBook aplica la regla de visibilidad de lectura: el autor ve el libro
// en cualquier estado, y cualquier otro solo si esta publicado. Un borrador
// ajeno responde NotFound, no PermissionDenied: que exista tampoco es publico.
func (s *LibraryService) visibleBook(ctx context.Context, bookID, callerID string) (*domain.Book, bool, error) {
	book, err := s.books.GetByID(ctx, bookID)
	if err != nil {
		return nil, false, mapRepoErr(err, "failed to load book")
	}

	isAuthor := callerID != "" && book.AuthorID == callerID
	if !isAuthor && book.Status != domain.BookStatusPublished {
		return nil, false, status.Error(codes.NotFound, "book not found")
	}
	return book, isAuthor, nil
}

// --- Validacion -------------------------------------------------------------

// requiredText valida un campo obligatorio de texto y lo deja sin espacios
// alrededor.
func requiredText(field, value string, maxLen int) (string, error) {
	v := strings.TrimSpace(value)
	if v == "" {
		return "", status.Errorf(codes.InvalidArgument, "%s is required", field)
	}
	if len(v) > maxLen {
		return "", status.Errorf(codes.InvalidArgument, "%s must be at most %d characters", field, maxLen)
	}
	return v, nil
}

// optionalText devuelve nil para el valor vacio, que es como se representa
// "sin dato" en las columnas nullables.
func optionalText(field, value string, maxLen int) (*string, error) {
	v := strings.TrimSpace(value)
	if v == "" {
		return nil, nil
	}
	if len(v) > maxLen {
		return nil, status.Errorf(codes.InvalidArgument, "%s must be at most %d characters", field, maxLen)
	}
	return &v, nil
}

// validateOptionalText valida el campo de un update parcial: aqui la cadena
// vacia si es significativa (limpia la columna), asi que no se convierte a nil.
func validateOptionalText(field string, value *string, maxLen int) (*string, error) {
	if value == nil {
		return nil, nil
	}
	v := strings.TrimSpace(*value)
	if len(v) > maxLen {
		return nil, status.Errorf(codes.InvalidArgument, "%s must be at most %d characters", field, maxLen)
	}
	return &v, nil
}

// validateRequiredUpdate es como validateOptionalText pero para campos que no
// admiten quedar vacios (el titulo es NOT NULL).
func validateRequiredUpdate(field string, value *string, maxLen int) (*string, error) {
	if value == nil {
		return nil, nil
	}
	v, err := requiredText(field, *value, maxLen)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

func validateBookStatus(value string) (string, error) {
	switch value {
	case domain.BookStatusDraft, domain.BookStatusPublished, domain.BookStatusArchived:
		return value, nil
	default:
		return "", status.Errorf(codes.InvalidArgument, "status must be one of: %s, %s, %s",
			domain.BookStatusDraft, domain.BookStatusPublished, domain.BookStatusArchived)
	}
}

func validateChapterStatus(value string) (string, error) {
	switch value {
	case domain.ChapterStatusDraft, domain.ChapterStatusPublished:
		return value, nil
	default:
		return "", status.Errorf(codes.InvalidArgument, "status must be one of: %s, %s",
			domain.ChapterStatusDraft, domain.ChapterStatusPublished)
	}
}

func validateSavedKind(value string) (string, error) {
	switch value {
	case domain.SavedKindFavorite, domain.SavedKindReadLater:
		return value, nil
	default:
		return "", status.Errorf(codes.InvalidArgument, "kind must be one of: %s, %s",
			domain.SavedKindFavorite, domain.SavedKindReadLater)
	}
}

// requiredID valida que un identificador venga presente. Que sea o no un UUID
// real lo resuelve el repositorio, que traduce el error del driver a NotFound:
// para una busqueda puntual, un id con forma invalida y uno que no existe son
// lo mismo.
func requiredID(field, value string) (string, error) {
	v := strings.TrimSpace(value)
	if v == "" {
		return "", status.Errorf(codes.InvalidArgument, "%s is required", field)
	}
	return v, nil
}

// optionalUUIDFilter valida un id que se usa como FILTRO de un listado, donde
// la regla anterior no sirve: Postgres rechaza el UUID malformado con un error
// que el repositorio traduce a NotFound, y un listado respondiendo
// "book not found" no tiene sentido. Se valida antes de llegar a la consulta.
func optionalUUIDFilter(field, value string) (string, error) {
	v := strings.TrimSpace(value)
	if v == "" {
		return "", nil
	}
	if !isUUID(v) {
		return "", status.Errorf(codes.InvalidArgument, "%s must be a valid UUID", field)
	}
	return v, nil
}

// isUUID comprueba la forma 8-4-4-4-12 en hexadecimal. Alcanza para no mandarle
// basura a Postgres, que es lo unico que se busca aqui; no valida la version
// del UUID ni hace falta.
func isUUID(v string) bool {
	if len(v) != 36 {
		return false
	}
	for i, c := range v {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
			if !isHex {
				return false
			}
		}
	}
	return true
}

// --- Errores ----------------------------------------------------------------

// mapRepoErr traduce los errores del repositorio a codigos gRPC. El fallback
// deja el detalle en el log y le devuelve al cliente un mensaje generico.
func mapRepoErr(err error, fallback string) error {
	switch {
	case errors.Is(err, repository.ErrBookNotFound):
		return status.Error(codes.NotFound, "book not found")
	case errors.Is(err, repository.ErrChapterNotFound):
		return status.Error(codes.NotFound, "chapter not found")
	case errors.Is(err, repository.ErrSagaNotFound):
		return status.Error(codes.NotFound, "saga not found")
	case errors.Is(err, repository.ErrSavedBookNotFound):
		return status.Error(codes.NotFound, "saved book not found")
	case errors.Is(err, repository.ErrAlreadySaved):
		return status.Error(codes.AlreadyExists, "book already saved with this kind")
	case errors.Is(err, repository.ErrBookAlreadyInSaga):
		return status.Error(codes.AlreadyExists, "book already belongs to this saga")
	case errors.Is(err, repository.ErrPositionTaken):
		return status.Error(codes.AlreadyExists, "another chapter already occupies that position")
	case errors.Is(err, repository.ErrLastChapter):
		return status.Error(codes.FailedPrecondition, "cannot delete the last chapter of a book")
	case errors.Is(err, repository.ErrReorderMismatch):
		return status.Error(codes.InvalidArgument, "the list must contain every item exactly once")
	default:
		log.Printf("repository error: %v", err)
		return status.Error(codes.Internal, fallback)
	}
}
