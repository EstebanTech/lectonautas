package service

import (
	"context"
	"fmt"
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

// --- Publicar un capitulo exige que el libro ya lo este ----------------------

// Publicar es "esto ya se puede leer". En un libro que no esta a la vista de
// nadie eso no significa nada, asi que se rechaza en vez de dejar el capitulo
// marcado como publico sin serlo.
func TestPublicarCapitulo_ExigeLibroPublicado(t *testing.T) {
	publicado := domain.ChapterStatusPublished

	for _, tt := range []struct {
		nombre string
		estado string
	}{
		{"borrador", domain.BookStatusDraft},
		{"archivado", domain.BookStatusArchived},
	} {
		t.Run("CreateChapter/libro "+tt.nombre, func(t *testing.T) {
			svc, _ := newTestService(asAuthor())
			id := bookConEstado(svc, tt.estado)

			_, err := svc.CreateChapter(context.Background(), &libraryv1.CreateChapterRequest{
				BookId: id, Title: "Capitulo", Status: publicado,
			})

			requireCode(t, err, codes.FailedPrecondition)
		})

		t.Run("UpdateChapter/libro "+tt.nombre, func(t *testing.T) {
			svc, _ := newTestService(asAuthor())
			id := bookConEstado(svc, tt.estado)
			ch := capituloDe(svc, id, domain.ChapterStatusDraft)

			_, err := svc.UpdateChapter(context.Background(), &libraryv1.UpdateChapterRequest{
				BookId: id, Id: ch, Status: &publicado,
			})

			requireCode(t, err, codes.FailedPrecondition)
		})
	}
}

// Con el libro publicado si se puede, que es la otra mitad de la regla.
func TestPublicarCapitulo_ConElLibroPublicadoSePuede(t *testing.T) {
	svc, _ := newTestService(asAuthor())
	publicado := domain.ChapterStatusPublished

	resp, err := svc.UpdateChapter(context.Background(), &libraryv1.UpdateChapterRequest{
		BookId: bookID, Id: chapter2ID, Status: &publicado,
	})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if resp.GetChapter().GetStatus() != domain.ChapterStatusPublished {
		t.Fatalf("status = %q", resp.GetChapter().GetStatus())
	}
}

// Un borrador si puede nacer o quedarse en borrador con el libro sin publicar:
// la regla solo mira la transicion a publicado.
func TestCapituloEnBorrador_SePuedeSiempre(t *testing.T) {
	svc, _ := newTestService(asAuthor())
	id := bookConEstado(svc, domain.BookStatusDraft)

	if _, err := svc.CreateChapter(context.Background(), &libraryv1.CreateChapterRequest{
		BookId: id, Title: "Escribiendo", Status: domain.ChapterStatusDraft,
	}); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
}

// La regla no traba el flujo de trabajo: para publicar el libro solo hace falta
// que TENGA capitulos, no que esten publicados. Si se exigiera lo segundo, no
// habria forma de publicar nada nunca.
func TestFlujoCompleto_BorradorAPublicado(t *testing.T) {
	svc, _ := newTestService(asAuthor())
	ctx := context.Background()
	id := bookConEstado(svc, domain.BookStatusDraft)

	// 1) Un capitulo en borrador, que es lo unico que deja el libro sin publicar.
	ch := capituloDe(svc, id, domain.ChapterStatusDraft)

	// 2) El libro se publica: le basta con tener contenido.
	publicadoLibro := domain.BookStatusPublished
	if _, err := svc.UpdateBook(ctx, &libraryv1.UpdateBookRequest{
		Id: id, Status: &publicadoLibro,
	}); err != nil {
		t.Fatalf("no se pudo publicar el libro con un capitulo en borrador: %v", err)
	}

	// 3) Y ahora si, el capitulo.
	publicadoCap := domain.ChapterStatusPublished
	if _, err := svc.UpdateChapter(ctx, &libraryv1.UpdateChapterRequest{
		BookId: id, Id: ch, Status: &publicadoCap,
	}); err != nil {
		t.Fatalf("con el libro ya publicado el capitulo deberia poder publicarse: %v", err)
	}

	// El conteo lo refleja: ahora hay uno para leer.
	libro, err := svc.GetBook(ctx, &libraryv1.GetBookRequest{Id: id})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if libro.GetBook().GetChapterCount() != 1 {
		t.Fatalf("chapter_count = %d, se esperaba 1", libro.GetBook().GetChapterCount())
	}
}

// Despublicar el libro no degrada sus capitulos: seria perder el estado de
// trabajo del autor, y no hace falta porque un libro sin publicar no lo ve
// nadie mas que el.
func TestDespublicarLibro_NoTocaSusCapitulos(t *testing.T) {
	svc, _ := newTestService(asAuthor())
	borrador := domain.BookStatusDraft

	if _, err := svc.UpdateBook(context.Background(), &libraryv1.UpdateBookRequest{
		Id: bookID, Status: &borrador,
	}); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	ch := svc.chapters.(*fakeChapterRepo).chapters[chapterID]
	if ch.Status != domain.ChapterStatusPublished {
		t.Fatalf("el capitulo quedo en %q; despublicar el libro no deberia tocarlo", ch.Status)
	}
}

// --- Fecha de publicacion ----------------------------------------------------

// publishedAt marca cuando la obra SALIO, no el ultimo cambio de estado: no se
// borra al despublicar ni se pisa al republicar. Si se pisara, bastaria con
// despublicar y republicar para colarse otra vez en las novedades.
func TestPublishedAt_SePonteUnaVezYNoSePisa(t *testing.T) {
	svc, _ := newTestService(asAuthor())
	ctx := context.Background()
	id := bookConEstado(svc, domain.BookStatusDraft)
	capituloDe(svc, id, domain.ChapterStatusDraft)

	// Un borrador todavia no tiene fecha.
	antes, err := svc.GetBook(ctx, &libraryv1.GetBookRequest{Id: id})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if antes.GetBook().GetPublishedAt() != nil {
		t.Fatal("un libro que nunca se publico no deberia traer publishedAt")
	}

	publicado := domain.BookStatusPublished
	borrador := domain.BookStatusDraft

	primera, err := svc.UpdateBook(ctx, &libraryv1.UpdateBookRequest{Id: id, Status: &publicado})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	original := primera.GetBook().GetPublishedAt()
	if original == nil {
		t.Fatal("al publicar tiene que quedar la fecha")
	}

	// Despublicar no la borra: es lo que distingue un borrador recien escrito
	// de uno que estuvo publicado y se retiro.
	despublicado, err := svc.UpdateBook(ctx, &libraryv1.UpdateBookRequest{Id: id, Status: &borrador})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if !despublicado.GetBook().GetPublishedAt().AsTime().Equal(original.AsTime()) {
		t.Fatal("despublicar no deberia borrar ni mover publishedAt")
	}

	// Y republicar tampoco la mueve.
	republicado, err := svc.UpdateBook(ctx, &libraryv1.UpdateBookRequest{Id: id, Status: &publicado})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if !republicado.GetBook().GetPublishedAt().AsTime().Equal(original.AsTime()) {
		t.Fatalf("republicar piso publishedAt: era %v y quedo %v",
			original.AsTime(), republicado.GetBook().GetPublishedAt().AsTime())
	}
}

// Los capitulos siguen la misma regla.
func TestPublishedAt_DelCapitulo(t *testing.T) {
	svc, _ := newTestService(asAuthor())
	ctx := context.Background()
	publicado := domain.ChapterStatusPublished
	borrador := domain.ChapterStatusDraft

	// chapter2ID nace en borrador en el escenario.
	primera, err := svc.UpdateChapter(ctx, &libraryv1.UpdateChapterRequest{
		BookId: bookID, Id: chapter2ID, Status: &publicado,
	})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	original := primera.GetChapter().GetPublishedAt()
	if original == nil {
		t.Fatal("al publicar el capitulo tiene que quedar la fecha")
	}

	if _, err := svc.UpdateChapter(ctx, &libraryv1.UpdateChapterRequest{
		BookId: bookID, Id: chapter2ID, Status: &borrador,
	}); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	republicado, err := svc.UpdateChapter(ctx, &libraryv1.UpdateChapterRequest{
		BookId: bookID, Id: chapter2ID, Status: &publicado,
	})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if !republicado.GetChapter().GetPublishedAt().AsTime().Equal(original.AsTime()) {
		t.Fatal("republicar el capitulo piso su publishedAt")
	}
}

// El escenario tiene un capitulo publicado y uno en borrador. chapter_count
// cuenta SOLO los publicados y da lo mismo quien pregunte: un borrador todavia
// no es parte del libro, asi que no suma para nadie — ni para su autor.
func TestChapterCount_SoloLosPublicadosParaTodos(t *testing.T) {
	for _, tt := range []struct {
		nombre string
		auth   fakeAuth
	}{
		{"el autor", asAuthor()},
		{"un lector ajeno", asIntruder()},
		{"un anonimo", anonymous()},
	} {
		t.Run(tt.nombre, func(t *testing.T) {
			svc, _ := newTestService(tt.auth)

			resp, err := svc.GetBook(context.Background(), &libraryv1.GetBookRequest{Id: bookID})
			if err != nil {
				t.Fatalf("error inesperado: %v", err)
			}

			if got := resp.GetBook().GetChapterCount(); got != 1 {
				t.Fatalf("chapter_count = %d, se esperaba 1 (hay 2 capitulos, 1 publicado)", got)
			}
		})
	}
}

// El autor sigue viendo sus borradores en la lista de capitulos; lo que ya no
// hace es contarlos. Los dos numeros dicen cosas distintas a proposito: uno es
// "cuanto hay para leer" y el otro "cuanto tengo escrito".
func TestChapterCount_ElAutorVeSusBorradoresAunqueNoCuenten(t *testing.T) {
	svc, _ := newTestService(asAuthor())

	resp, err := svc.GetBook(context.Background(), &libraryv1.GetBookRequest{Id: bookID})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if len(resp.GetChapters()) != 2 {
		t.Fatalf("el autor deberia seguir viendo sus 2 capitulos, vio %d", len(resp.GetChapters()))
	}
	if resp.GetBook().GetChapterCount() != 1 {
		t.Fatalf("chapter_count = %d, se esperaba 1", resp.GetBook().GetChapterCount())
	}
}

// Y el mismo numero en los dos listados, que antes diferian.
func TestChapterCount_IgualEnLosDosListados(t *testing.T) {
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
	if len(propio.GetBooks()) != 1 || propio.GetBooks()[0].GetChapterCount() != 1 {
		t.Fatalf("en su propio listado el autor tambien deberia ver chapter_count 1")
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

// El listado de lo propio no se pagina: trae la obra entera aunque pase del
// tamano de pagina con el que se recorta el publico.
func TestListMyBooks_NoSePagina(t *testing.T) {
	svc, _ := newTestService(asAuthor())
	ctx := context.Background()

	// Bastantes mas que defaultPageSize, para que un recorte por pagina se note.
	const cuantos = defaultPageSize + 12
	repo := svc.books.(*fakeBookRepo)
	for i := 0; i < cuantos; i++ {
		id := fmt.Sprintf("aaaaaaaa-0000-0000-0000-%012d", i)
		repo.books[id] = &domain.Book{
			ID: id, AuthorID: authorID, Title: "Suyo", Status: domain.BookStatusPublished,
		}
	}
	// Mas el que ya trae el escenario.
	esperados := cuantos + 1

	resp, err := svc.ListMyBooks(ctx, &libraryv1.ListMyBooksRequest{})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if len(resp.GetBooks()) != esperados {
		t.Fatalf("libros = %d, se esperaban los %d: este listado no pagina",
			len(resp.GetBooks()), esperados)
	}
	if resp.GetTotal() != int32(esperados) {
		t.Fatalf("total = %d, se esperaban %d", resp.GetTotal(), esperados)
	}

	// Y el publico sigue paginando, que es de lo que este se separa.
	publico, err := svc.ListBooks(ctx, &libraryv1.ListBooksRequest{})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if len(publico.GetBooks()) != defaultPageSize {
		t.Fatalf("el listado publico trajo %d libros, se esperaba una pagina de %d",
			len(publico.GetBooks()), defaultPageSize)
	}
	if publico.GetTotal() != int32(esperados) {
		t.Fatalf("total del publico = %d, se esperaban %d: el total no es el de la pagina",
			publico.GetTotal(), esperados)
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
