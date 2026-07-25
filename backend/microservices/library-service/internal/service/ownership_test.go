package service

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"

	"github.com/estebandeveloper20/lectonautas/backend/microservices/library-service/internal/domain"
	libraryv1 "github.com/estebandeveloper20/lectonautas/backend/microservices/library-service/proto/library/v1"
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
			_, err := s.DeleteChapter(ctx, &libraryv1.DeleteChapterRequest{BookId: bookID, Id: chapterID})
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

func TestDeleteChapter_NoSePuedeBorrarElUltimo(t *testing.T) {
	svc, _ := newTestService(asAuthor())
	ctx := context.Background()

	// Quedan dos: borrar uno tiene que funcionar.
	if _, err := svc.DeleteChapter(ctx, &libraryv1.DeleteChapterRequest{BookId: bookID, Id: chapter2ID}); err != nil {
		t.Fatalf("con dos capitulos deberia poder borrar uno: %v", err)
	}

	// Queda uno: el libro no puede quedarse sin capitulos.
	_, err := svc.DeleteChapter(ctx, &libraryv1.DeleteChapterRequest{BookId: bookID, Id: chapterID})
	requireCode(t, err, codes.FailedPrecondition)
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

func TestCreateBook_SiempreNaceConUnCapitulo(t *testing.T) {
	svc, _ := newTestService(asAuthor())

	// Sin first_chapter: la regla es que igual se cree uno.
	_, err := svc.CreateBook(context.Background(), &libraryv1.CreateBookRequest{Title: "Nuevo"})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	chapters, _ := svc.chapters.ListByBook(context.Background(), bookID, false)
	if len(chapters) == 0 {
		t.Fatal("el libro se creo sin capitulos")
	}
}

func TestCreateBook_PublicadoNaceConSuCapituloPublicado(t *testing.T) {
	svc, _ := newTestService(asAuthor())

	_, err := svc.CreateBook(context.Background(), &libraryv1.CreateBookRequest{
		Title:  "Publicado de una",
		Status: domain.BookStatusPublished,
	})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	// Si el capitulo naciera en borrador, el lector veria el libro con la
	// lista de capitulos vacia.
	ch, err := svc.chapters.GetByID(context.Background(), bookID, chapterID)
	if err != nil {
		t.Fatalf("no se encontro el capitulo inicial: %v", err)
	}
	if ch.Status != domain.ChapterStatusPublished {
		t.Fatalf("el capitulo inicial quedo en %q", ch.Status)
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
