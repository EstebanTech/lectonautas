package service

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"

	"github.com/EstebanTech/lectonautas/backend/microservices/library-service/internal/domain"
	libraryv1 "github.com/EstebanTech/lectonautas/backend/microservices/library-service/proto/library/v1"
)

// Toda escritura sobre libros, capitulos y sagas exige ser el autor. Estas
// pruebas recorren cada una con el token de otro usuario.

func TestEscrituras_SoloElAutor(t *testing.T) {
	title := "secuestrado"

	tests := []struct {
		nombre string
		llamar func(context.Context, *LibraryService) error
	}{
		{"UpdateBook", func(ctx context.Context, s *LibraryService) error {
			_, err := s.UpdateBook(ctx, &libraryv1.UpdateBookRequest{Id: bookID, Title: &title})
			return err
		}},
		{"DeleteBook", func(ctx context.Context, s *LibraryService) error {
			_, err := s.DeleteBook(ctx, &libraryv1.DeleteBookRequest{Id: bookID})
			return err
		}},
		{"CreateChapter", func(ctx context.Context, s *LibraryService) error {
			_, err := s.CreateChapter(ctx, &libraryv1.CreateChapterRequest{BookId: bookID, Title: "nuevo"})
			return err
		}},
		{"UpdateChapter", func(ctx context.Context, s *LibraryService) error {
			_, err := s.UpdateChapter(ctx, &libraryv1.UpdateChapterRequest{BookId: bookID, Id: chapterID, Title: &title})
			return err
		}},
		{"DeleteChapter", func(ctx context.Context, s *LibraryService) error {
			// El capitulo en borrador, no el publicado: aqui se prueba quien
			// puede borrar, no la regla del ultimo capitulo publicado.
			_, err := s.DeleteChapter(ctx, &libraryv1.DeleteChapterRequest{BookId: bookID, Id: chapter2ID})
			return err
		}},
		{"ReorderChapters", func(ctx context.Context, s *LibraryService) error {
			_, err := s.ReorderChapters(ctx, &libraryv1.ReorderChaptersRequest{
				BookId: bookID, ChapterIds: []string{chapterID, chapter2ID},
			})
			return err
		}},
		{"UpdateSaga", func(ctx context.Context, s *LibraryService) error {
			_, err := s.UpdateSaga(ctx, &libraryv1.UpdateSagaRequest{Id: sagaID, Title: &title})
			return err
		}},
		{"DeleteSaga", func(ctx context.Context, s *LibraryService) error {
			_, err := s.DeleteSaga(ctx, &libraryv1.DeleteSagaRequest{Id: sagaID})
			return err
		}},
		{"AddBookToSaga", func(ctx context.Context, s *LibraryService) error {
			_, err := s.AddBookToSaga(ctx, &libraryv1.AddBookToSagaRequest{SagaId: sagaID, BookId: bookID})
			return err
		}},
		{"RemoveBookFromSaga", func(ctx context.Context, s *LibraryService) error {
			_, err := s.RemoveBookFromSaga(ctx, &libraryv1.RemoveBookFromSagaRequest{SagaId: sagaID, BookId: bookID})
			return err
		}},
	}

	for _, tt := range tests {
		t.Run(tt.nombre+"_intrusoRecibePermissionDenied", func(t *testing.T) {
			svc, _ := newTestService(asIntruder())
			requireCode(t, tt.llamar(context.Background(), svc), codes.PermissionDenied)
		})

		t.Run(tt.nombre+"_sinTokenRecibeUnauthenticated", func(t *testing.T) {
			svc, _ := newTestService(anonymous())
			requireCode(t, tt.llamar(context.Background(), svc), codes.Unauthenticated)
		})

		t.Run(tt.nombre+"_elAutorPuede", func(t *testing.T) {
			svc, _ := newTestService(asAuthor())
			if err := tt.llamar(context.Background(), svc); err != nil {
				t.Fatalf("el autor deberia poder: %v", err)
			}
		})
	}
}

func TestDeleteChapter_NoDejaVacioUnLibroPublicado(t *testing.T) {
	svc, _ := newTestService(asAuthor())
	ctx := context.Background()

	// Quedan dos: borrar uno no vacia el libro.
	if _, err := svc.DeleteChapter(ctx, &libraryv1.DeleteChapterRequest{BookId: bookID, Id: chapter2ID}); err != nil {
		t.Fatalf("con dos capitulos deberia poder borrar uno: %v", err)
	}

	// El ultimo que queda, no: dejaria publicado un libro vacio.
	_, err := svc.DeleteChapter(ctx, &libraryv1.DeleteChapterRequest{BookId: bookID, Id: chapterID})
	requireCode(t, err, codes.FailedPrecondition)
}

// Un libro en borrador si se puede vaciar del todo: es la contraparte de que
// ahora nazca vacio.
func TestDeleteChapter_UnBorradorSePuedeVaciar(t *testing.T) {
	svc, _ := newTestService(asAuthor())
	ctx := context.Background()

	id := draftBook(svc)
	chapters := svc.chapters.(*fakeChapterRepo)
	const soloUno = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	chapters.chapters[soloUno] = &domain.Chapter{
		ID: soloUno, BookID: id, Title: "Unico", Position: 1, Status: domain.ChapterStatusPublished,
	}

	if _, err := svc.DeleteChapter(ctx, &libraryv1.DeleteChapterRequest{BookId: id, Id: soloUno}); err != nil {
		t.Fatalf("un libro en borrador deberia poder quedarse sin capitulos: %v", err)
	}
}

// Despublicar un capitulo no vacia el libro: la fila sigue ahi. Que el autor
// tenga un libro publicado con los capitulos aun en borrador es cosa suya.
func TestUpdateChapter_DespublicarElUltimoEstaPermitido(t *testing.T) {
	svc, _ := newTestService(asAuthor())
	draft := domain.ChapterStatusDraft

	_, err := svc.UpdateChapter(context.Background(), &libraryv1.UpdateChapterRequest{
		BookId: bookID, Id: chapterID, Status: &draft,
	})

	if err != nil {
		t.Fatalf("despublicar un capitulo no deberia estar bloqueado: %v", err)
	}
}

// DeleteAuthorContent es lo que llama user-service al dar de baja una cuenta:
// se lleva la obra de ese autor y solo la de ese autor.
func TestDeleteAuthorContent_SoloArrastraLoDelAutor(t *testing.T) {
	svc, cache := newTestService(asAuthor())
	ctx := context.Background()

	const ajenoID = "77777777-7777-7777-7777-777777777777"
	svc.books.(*fakeBookRepo).books[ajenoID] = &domain.Book{
		ID: ajenoID, AuthorID: intruderID, Title: "De otro", Status: domain.BookStatusPublished,
	}

	resp, err := svc.DeleteAuthorContent(ctx, &libraryv1.DeleteAuthorContentRequest{UserId: authorID})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if resp.GetBooksDeleted() != 1 {
		t.Fatalf("libros borrados = %d, se esperaba 1", resp.GetBooksDeleted())
	}

	libros := svc.books.(*fakeBookRepo).books
	if _, sigue := libros[bookID]; sigue {
		t.Fatal("el libro del autor dado de baja sigue ahi")
	}
	if _, sigue := libros[ajenoID]; !sigue {
		t.Fatal("se borro el libro de otro autor")
	}
	// Los listados cacheados quedaron viejos: les sobra la obra borrada.
	if cache.bumps == 0 {
		t.Fatal("la baja no invalido el cache")
	}
}

func TestDeleteAuthorContent_ExigeUserID(t *testing.T) {
	svc, _ := newTestService(asAuthor())

	_, err := svc.DeleteAuthorContent(context.Background(), &libraryv1.DeleteAuthorContentRequest{})

	requireCode(t, err, codes.InvalidArgument)
}

// Un libro vacio no sale del borrador, pero se borra sin ninguna traba: no hay
// forma de quedarse con un libro que no se puede publicar ni eliminar.
func TestDeleteBook_UnLibroVacioSePuedeBorrar(t *testing.T) {
	svc, _ := newTestService(asAuthor())

	vacio := draftBook(svc)
	if _, err := svc.DeleteBook(context.Background(), &libraryv1.DeleteBookRequest{Id: vacio}); err != nil {
		t.Fatalf("un libro vacio deberia poder borrarse: %v", err)
	}
}

func TestDeleteChapter_CapituloInexistenteEs404NoUltimoCapitulo(t *testing.T) {
	svc, _ := newTestService(asAuthor())

	// Un id inventado sobre un libro con capitulos no debe confundirse con
	// "es el ultimo": son errores distintos y el mensaje importa.
	_, err := svc.DeleteChapter(context.Background(), &libraryv1.DeleteChapterRequest{
		BookId: bookID, Id: missingID,
	})

	requireCode(t, err, codes.NotFound)
}

func TestReorderChapters_RechazaListaInvalida(t *testing.T) {
	tests := []struct {
		nombre string
		ids    []string
	}{
		{"vacia", []string{}},
		{"con duplicados", []string{chapterID, chapterID}},
		{"con un id vacio", []string{chapterID, ""}},
	}

	for _, tt := range tests {
		t.Run(tt.nombre, func(t *testing.T) {
			svc, _ := newTestService(asAuthor())

			_, err := svc.ReorderChapters(context.Background(), &libraryv1.ReorderChaptersRequest{
				BookId: bookID, ChapterIds: tt.ids,
			})

			requireCode(t, err, codes.InvalidArgument)
		})
	}
}

func TestReorderChapters_RechazaListaIncompleta(t *testing.T) {
	svc, _ := newTestService(asAuthor())

	// Faltando uno, el reorder dejaria capitulos con posiciones incoherentes.
	_, err := svc.ReorderChapters(context.Background(), &libraryv1.ReorderChaptersRequest{
		BookId: bookID, ChapterIds: []string{chapterID},
	})

	requireCode(t, err, codes.InvalidArgument)
}

func TestCreateBook_NaceVacioYEnBorrador(t *testing.T) {
	svc, _ := newTestService(asAuthor())
	ctx := context.Background()

	resp, err := svc.CreateBook(ctx, &libraryv1.CreateBookRequest{Title: "Nuevo"})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if resp.GetBook().GetStatus() != domain.BookStatusDraft {
		t.Fatalf("status = %q, se esperaba %q", resp.GetBook().GetStatus(), domain.BookStatusDraft)
	}

	// El contenido llega despues, por CreateChapter.
	chapters, _ := svc.chapters.ListByBook(ctx, resp.GetBook().GetId(), false)
	if len(chapters) != 0 {
		t.Fatalf("el libro nacio con %d capitulos, se esperaba ninguno", len(chapters))
	}
}

// Nacer publicado dejaria a la vista un libro vacio, asi que se rechaza en vez
// de degradarlo a borrador en silencio.
func TestCreateBook_NoPuedeNacerPublicado(t *testing.T) {
	svc, _ := newTestService(asAuthor())

	_, err := svc.CreateBook(context.Background(), &libraryv1.CreateBookRequest{
		Title:  "Publicado de una",
		Status: domain.BookStatusPublished,
	})

	requireCode(t, err, codes.FailedPrecondition)
}

func TestUpdateBook_UnLibroVacioNoSePublica(t *testing.T) {
	svc, _ := newTestService(asAuthor())
	ctx := context.Background()
	published := domain.BookStatusPublished

	// draftBook no tiene capitulos: publicarlo tiene que fallar, sin importar
	// cuantas veces se intente.
	vacio := draftBook(svc)
	_, err := svc.UpdateBook(ctx, &libraryv1.UpdateBookRequest{Id: vacio, Status: &published})
	requireCode(t, err, codes.FailedPrecondition)

	// Un capitulo, aunque este en borrador, ya lo habilita: el libro dejo de
	// estar vacio y publicar la ficha antes que los capitulos es decision del
	// autor.
	chapters := svc.chapters.(*fakeChapterRepo)
	const enBorrador = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	chapters.chapters[enBorrador] = &domain.Chapter{
		ID: enBorrador, BookID: vacio, Title: "Uno", Position: 1, Status: domain.ChapterStatusDraft,
	}

	if _, err := svc.UpdateBook(ctx, &libraryv1.UpdateBookRequest{Id: vacio, Status: &published}); err != nil {
		t.Fatalf("con un capitulo deberia poder publicarse: %v", err)
	}
}

func TestCreateBook_ValidaEntrada(t *testing.T) {
	tests := []struct {
		nombre string
		req    *libraryv1.CreateBookRequest
	}{
		{"titulo vacio", &libraryv1.CreateBookRequest{Title: "   "}},
		{"estado invalido", &libraryv1.CreateBookRequest{Title: "x", Status: "inventado"}},
	}

	for _, tt := range tests {
		t.Run(tt.nombre, func(t *testing.T) {
			svc, _ := newTestService(asAuthor())
			_, err := svc.CreateBook(context.Background(), tt.req)
			requireCode(t, err, codes.InvalidArgument)
		})
	}
}

func TestEscrituras_InvalidanElCache(t *testing.T) {
	svc, c := newTestService(asAuthor())
	title := "nuevo titulo"

	if _, err := svc.UpdateBook(context.Background(), &libraryv1.UpdateBookRequest{Id: bookID, Title: &title}); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	// Sin este bump, los GET seguirian sirviendo el titulo viejo hasta que
	// venza el TTL de 15 minutos.
	if c.bumps != 1 {
		t.Fatalf("invalidaciones = %d, se esperaba 1", c.bumps)
	}
}

func TestSaveBook_ValidaKindYExigeToken(t *testing.T) {
	t.Run("kind invalido", func(t *testing.T) {
		svc, _ := newTestService(asAuthor())
		_, err := svc.SaveBook(context.Background(), &libraryv1.SaveBookRequest{
			BookId: bookID, Kind: "inventado",
		})
		requireCode(t, err, codes.InvalidArgument)
	})

	t.Run("sin token", func(t *testing.T) {
		svc, _ := newTestService(anonymous())
		_, err := svc.SaveBook(context.Background(), &libraryv1.SaveBookRequest{
			BookId: bookID, Kind: domain.SavedKindFavorite,
		})
		requireCode(t, err, codes.Unauthenticated)
	})

	t.Run("no se puede guardar un borrador ajeno", func(t *testing.T) {
		svc, _ := newTestService(asIntruder())
		id := draftBook(svc)
		_, err := svc.SaveBook(context.Background(), &libraryv1.SaveBookRequest{
			BookId: id, Kind: domain.SavedKindFavorite,
		})
		requireCode(t, err, codes.NotFound)
	})
}

func TestListSavedBooks_SoloLaBibliotecaPropia(t *testing.T) {
	svc, _ := newTestService(asAuthor())
	ctx := context.Background()

	if _, err := svc.SaveBook(ctx, &libraryv1.SaveBookRequest{
		BookId: bookID, Kind: domain.SavedKindFavorite,
	}); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	// El mismo servicio, pero con otro llamante: no debe ver nada.
	svc.auth = asIntruder()
	resp, err := svc.ListSavedBooks(ctx, &libraryv1.ListSavedBooksRequest{})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if resp.GetTotal() != 0 {
		t.Fatalf("el intruso vio %d entradas de la biblioteca ajena", resp.GetTotal())
	}
}

func TestListMySagas_ExigeTokenYAislaPorAutor(t *testing.T) {
	t.Run("sin token", func(t *testing.T) {
		svc, _ := newTestService(anonymous())
		_, err := svc.ListMySagas(context.Background(), &libraryv1.ListMySagasRequest{})
		requireCode(t, err, codes.Unauthenticated)
	})

	t.Run("el intruso no ve las ajenas", func(t *testing.T) {
		svc, _ := newTestService(asIntruder())
		resp, err := svc.ListMySagas(context.Background(), &libraryv1.ListMySagasRequest{})
		if err != nil {
			t.Fatalf("error inesperado: %v", err)
		}
		if resp.GetTotal() != 0 {
			t.Fatalf("el intruso vio %d sagas ajenas", resp.GetTotal())
		}
	})

	t.Run("el autor ve las suyas", func(t *testing.T) {
		svc, _ := newTestService(asAuthor())
		resp, err := svc.ListMySagas(context.Background(), &libraryv1.ListMySagasRequest{})
		if err != nil {
			t.Fatalf("error inesperado: %v", err)
		}
		if resp.GetTotal() != 1 {
			t.Fatalf("total = %d, se esperaba 1", resp.GetTotal())
		}
	})
}
