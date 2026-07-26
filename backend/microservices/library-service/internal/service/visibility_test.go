package service

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/EstebanTech/lectonautas/backend/microservices/library-service/internal/domain"
	libraryv1 "github.com/EstebanTech/lectonautas/backend/microservices/library-service/proto/library/v1"
)

// Estas pruebas cubren las reglas que hacen que un borrador no se filtre y que
// nadie escriba sobre obra ajena. Son las que mas caro sale romper en silencio.

func requireCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("se esperaba error con codigo %v, no hubo error", want)
	}
	if got := status.Code(err); got != want {
		t.Fatalf("codigo = %v, se esperaba %v (error: %v)", got, want, err)
	}
}

func TestGetBook_BorradorSoloLoVeSuAutor(t *testing.T) {
	tests := []struct {
		nombre   string
		llamante fakeAuth
		wantErr  bool
	}{
		{"el autor lo ve", asAuthor(), false},
		{"otro usuario no", asIntruder(), true},
		{"un anonimo tampoco", anonymous(), true},
	}

	for _, tt := range tests {
		t.Run(tt.nombre, func(t *testing.T) {
			svc, _ := newTestService(tt.llamante)
			id := draftBook(svc)

			_, err := svc.GetBook(context.Background(), &libraryv1.GetBookRequest{Id: id})

			if !tt.wantErr {
				if err != nil {
					t.Fatalf("el autor deberia ver su borrador: %v", err)
				}
				return
			}
			// NotFound y no PermissionDenied: que el borrador exista tampoco
			// es publico.
			requireCode(t, err, codes.NotFound)
		})
	}
}

func TestGetBook_LectorAjenoNoVeCapitulosEnBorrador(t *testing.T) {
	svc, _ := newTestService(asIntruder())

	resp, err := svc.GetBook(context.Background(), &libraryv1.GetBookRequest{Id: bookID})
	if err != nil {
		t.Fatalf("el libro esta publicado, deberia verse: %v", err)
	}

	for _, ch := range resp.GetChapters() {
		if ch.GetStatus() != domain.ChapterStatusPublished {
			t.Fatalf("se filtro un capitulo en %q a un lector ajeno", ch.GetStatus())
		}
	}
	if len(resp.GetChapters()) != 1 {
		t.Fatalf("capitulos visibles = %d, se esperaba 1", len(resp.GetChapters()))
	}
}

func TestGetBook_ElAutorVeSusCapitulosEnBorrador(t *testing.T) {
	svc, _ := newTestService(asAuthor())

	resp, err := svc.GetBook(context.Background(), &libraryv1.GetBookRequest{Id: bookID})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if len(resp.GetChapters()) != 2 {
		t.Fatalf("el autor deberia ver sus 2 capitulos, vio %d", len(resp.GetChapters()))
	}
}

// El escenario tiene un capitulo publicado y uno en borrador: chapter_count
// tiene que cuadrar con los capitulos que cada uno puede abrir, o el numero
// delataria obra inedita.
func TestChapterCount_SigueLaMismaVisibilidadQueLosCapitulos(t *testing.T) {
	tests := []struct {
		nombre string
		auth   fakeAuth
		quiere int32
	}{
		{"el autor cuenta tambien sus borradores", asAuthor(), 2},
		{"un lector ajeno solo cuenta los publicados", asIntruder(), 1},
		{"un anonimo, igual que el lector ajeno", anonymous(), 1},
	}

	for _, tt := range tests {
		t.Run(tt.nombre, func(t *testing.T) {
			svc, _ := newTestService(tt.auth)

			resp, err := svc.GetBook(context.Background(), &libraryv1.GetBookRequest{Id: bookID})
			if err != nil {
				t.Fatalf("error inesperado: %v", err)
			}

			if got := resp.GetBook().GetChapterCount(); got != tt.quiere {
				t.Fatalf("chapter_count = %d, se esperaba %d", got, tt.quiere)
			}
			// El numero no puede contradecir a la lista que viaja al lado.
			if got := int32(len(resp.GetChapters())); got != resp.GetBook().GetChapterCount() {
				t.Fatalf("chapter_count = %d pero llegaron %d capitulos",
					resp.GetBook().GetChapterCount(), got)
			}
		})
	}
}

// El listado publico da el numero publico incluso al autor: es la vista de
// afuera. La suya, con borradores, esta en ListMyBooks.
func TestChapterCount_ListadoPublicoVsElDelAutor(t *testing.T) {
	svc, _ := newTestService(asAuthor())
	ctx := context.Background()

	publico, err := svc.ListBooks(ctx, &libraryv1.ListBooksRequest{})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if len(publico.GetBooks()) != 1 || publico.GetBooks()[0].GetChapterCount() != 1 {
		t.Fatalf("en el listado publico se esperaba un libro con chapter_count 1")
	}

	propio, err := svc.ListMyBooks(ctx, &libraryv1.ListMyBooksRequest{})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if len(propio.GetBooks()) != 1 || propio.GetBooks()[0].GetChapterCount() != 2 {
		t.Fatalf("en su propio listado el autor deberia ver chapter_count 2")
	}
}

func TestListBooks_SiempreFiltraPorPublicados(t *testing.T) {
	// Incluso con el token del autor: este listado es el publico.
	svc, _ := newTestService(asAuthor())
	draftBook(svc)

	resp, err := svc.ListBooks(context.Background(), &libraryv1.ListBooksRequest{})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	for _, b := range resp.GetBooks() {
		if b.GetStatus() != domain.BookStatusPublished {
			t.Fatalf("el listado publico devolvio un libro en %q", b.GetStatus())
		}
	}
}

func TestListBooks_RechazaPedirOtroEstado(t *testing.T) {
	svc, _ := newTestService(asAuthor())

	_, err := svc.ListBooks(context.Background(), &libraryv1.ListBooksRequest{
		Status: domain.BookStatusDraft,
	})

	// Se rechaza en vez de devolver publicados en silencio: si no, el autor
	// creeria que no tiene borradores.
	requireCode(t, err, codes.InvalidArgument)
}

func TestListBooks_AuthorIdMalformadoNoEs404(t *testing.T) {
	svc, _ := newTestService(anonymous())

	_, err := svc.ListBooks(context.Background(), &libraryv1.ListBooksRequest{
		AuthorId: invalidUUID,
	})

	// Antes llegaba a Postgres, fallaba con 22P02 y salia como
	// NotFound "book not found", que en un listado no significa nada.
	requireCode(t, err, codes.InvalidArgument)
}

func TestListMyBooks_DevuelveTodosLosEstadosDelAutor(t *testing.T) {
	svc, _ := newTestService(asAuthor())
	draftBook(svc)

	resp, err := svc.ListMyBooks(context.Background(), &libraryv1.ListMyBooksRequest{})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if resp.GetTotal() != 2 {
		t.Fatalf("total = %d, se esperaban 2 (el publicado y el borrador)", resp.GetTotal())
	}
}

func TestListMyBooks_ExigeToken(t *testing.T) {
	svc, _ := newTestService(anonymous())

	_, err := svc.ListMyBooks(context.Background(), &libraryv1.ListMyBooksRequest{})

	requireCode(t, err, codes.Unauthenticated)
}

func TestListMyBooks_NuncaDevuelveObraDeOtro(t *testing.T) {
	// El intruso tiene sesion valida, pero no libros: el author_id sale del
	// token, asi que no hay parametro que manipular para ver los ajenos.
	svc, _ := newTestService(asIntruder())
	draftBook(svc)

	resp, err := svc.ListMyBooks(context.Background(), &libraryv1.ListMyBooksRequest{})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if resp.GetTotal() != 0 {
		t.Fatalf("el intruso vio %d libros ajenos", resp.GetTotal())
	}
}

func TestListMyBooks_TokenInvalidoNoDegradaALectorAnonimo(t *testing.T) {
	svc, _ := newTestService(fakeAuth{invalid: true})

	_, err := svc.ListMyBooks(context.Background(), &libraryv1.ListMyBooksRequest{})

	requireCode(t, err, codes.Unauthenticated)
}

func TestGetBook_TokenInvalidoEsError(t *testing.T) {
	// Una lectura publica con un token vencido tiene que avisar, no responder
	// como si no se hubiera enviado nada.
	svc, _ := newTestService(fakeAuth{invalid: true})

	_, err := svc.GetBook(context.Background(), &libraryv1.GetBookRequest{Id: bookID})

	requireCode(t, err, codes.Unauthenticated)
}
