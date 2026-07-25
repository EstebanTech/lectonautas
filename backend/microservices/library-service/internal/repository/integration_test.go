package repository

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/estebandeveloper20/lectonautas/backend/microservices/library-service/internal/domain"
)

// Pruebas contra una base real. Cubren lo que los dobles en memoria no pueden:
// transacciones, CASCADE, los UNIQUE del esquema y la traduccion de los errores
// del driver a errores de dominio.
//
// Se saltan solas si no hay base configurada, para que `go test ./...` siga
// sirviendo sin levantar nada:
//
//	LIBRARY_TEST_DATABASE_URL="postgres://user:password@localhost:5433/library_service?sslmode=disable" go test ./internal/repository/
//
// Cada prueba usa su propio author_id y borra sus libros al terminar, asi que
// puede correr contra la base de desarrollo sin ensuciarla.

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	url := os.Getenv("LIBRARY_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("LIBRARY_TEST_DATABASE_URL no esta definida; se omiten las pruebas de integracion")
	}

	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("no se pudo abrir el pool: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		t.Fatalf("no se pudo conectar a la base: %v", err)
	}
	t.Cleanup(pool.Close)

	return pool
}

// newAuthor devuelve un author_id nuevo y deja programada la limpieza de todo
// lo que cuelgue de el.
func newAuthor(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()

	var id string
	if err := pool.QueryRow(context.Background(), `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		t.Fatalf("no se pudo generar el author_id: %v", err)
	}

	t.Cleanup(func() {
		ctx := context.Background()
		// Los capitulos, los vinculos de saga y lo guardado por los lectores se
		// van por CASCADE al borrar el libro.
		pool.Exec(ctx, `DELETE FROM content.books WHERE author_id = $1`, id)
		pool.Exec(ctx, `DELETE FROM content.sagas WHERE author_id = $1`, id)
		pool.Exec(ctx, `DELETE FROM reader.saved_books WHERE user_id = $1`, id)
	})

	return id
}

func newBook(t *testing.T, repo *PostgresBookRepository, authorID, title, bookStatus string) *domain.Book {
	t.Helper()

	book, _, err := repo.CreateWithFirstChapter(context.Background(),
		&domain.Book{AuthorID: authorID, Title: title, Status: bookStatus},
		&domain.Chapter{Title: "Capitulo 1", Status: domain.ChapterStatusDraft},
	)
	if err != nil {
		t.Fatalf("no se pudo crear el libro: %v", err)
	}
	return book
}

func TestCreateWithFirstChapter_EsTransaccional(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	books := NewPostgresBookRepository(pool)
	chapters := NewPostgresChapterRepository(pool)
	author := newAuthor(t, pool)

	book, chapter, err := books.CreateWithFirstChapter(ctx,
		&domain.Book{AuthorID: author, Title: "Con capitulo", Status: domain.BookStatusDraft},
		&domain.Chapter{Title: "El embarque", Status: domain.ChapterStatusDraft},
	)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if chapter.Position != 1 {
		t.Fatalf("position del primer capitulo = %d, se esperaba 1", chapter.Position)
	}

	got, err := chapters.ListByBook(ctx, book.ID, false)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("capitulos = %d, se esperaba 1", len(got))
	}
}

func TestCreateWithFirstChapter_RevierteSiFallaElCapitulo(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	books := NewPostgresBookRepository(pool)
	author := newAuthor(t, pool)

	// El titulo del capitulo es NOT NULL y VARCHAR(255): uno mas largo hace
	// fallar el segundo INSERT, y el libro no debe sobrevivir.
	tituloImposible := make([]byte, 300)
	for i := range tituloImposible {
		tituloImposible[i] = 'x'
	}

	_, _, err := books.CreateWithFirstChapter(ctx,
		&domain.Book{AuthorID: author, Title: "No deberia quedar", Status: domain.BookStatusDraft},
		&domain.Chapter{Title: string(tituloImposible), Status: domain.ChapterStatusDraft},
	)
	if err == nil {
		t.Fatal("se esperaba que fallara la insercion del capitulo")
	}

	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM content.books WHERE author_id = $1`, author).Scan(&n); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if n != 0 {
		t.Fatalf("quedaron %d libros sin capitulos: la transaccion no revirtio", n)
	}
}

func TestDeleteBook_ArrastraCapitulosYGuardados(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	books := NewPostgresBookRepository(pool)
	saved := NewPostgresSavedBookRepository(pool)
	author := newAuthor(t, pool)

	book := newBook(t, books, author, "Para borrar", domain.BookStatusPublished)
	if _, err := saved.Save(ctx, author, book.ID, domain.SavedKindFavorite); err != nil {
		t.Fatalf("no se pudo guardar el libro: %v", err)
	}

	if err := books.Delete(ctx, book.ID); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	for _, q := range []struct {
		nombre string
		query  string
	}{
		{"capitulos", `SELECT count(*) FROM content.chapters WHERE book_id = $1`},
		{"guardados", `SELECT count(*) FROM reader.saved_books WHERE book_id = $1`},
	} {
		var n int
		if err := pool.QueryRow(ctx, q.query, book.ID).Scan(&n); err != nil {
			t.Fatalf("error inesperado: %v", err)
		}
		if n != 0 {
			t.Fatalf("quedaron %d %s tras borrar el libro", n, q.nombre)
		}
	}
}

func TestCreateChapter_PosicionCeroAgregaAlFinal(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	books := NewPostgresBookRepository(pool)
	chapters := NewPostgresChapterRepository(pool)
	author := newAuthor(t, pool)

	book := newBook(t, books, author, "Con varios", domain.BookStatusDraft)

	for i, want := range []int32{2, 3, 4} {
		ch, err := chapters.Create(ctx, &domain.Chapter{
			BookID: book.ID, Title: "Siguiente", Position: 0, Status: domain.ChapterStatusDraft,
		})
		if err != nil {
			t.Fatalf("capitulo %d: %v", i, err)
		}
		if ch.Position != want {
			t.Fatalf("position = %d, se esperaba %d", ch.Position, want)
		}
	}
}

func TestCreateChapter_PosicionOcupadaDaErrorDeDominio(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	books := NewPostgresBookRepository(pool)
	chapters := NewPostgresChapterRepository(pool)
	author := newAuthor(t, pool)

	book := newBook(t, books, author, "Choque", domain.BookStatusDraft)

	// La posicion 1 ya la ocupa el capitulo inicial. El UNIQUE del esquema
	// tiene que llegar al servicio como ErrPositionTaken, no como error crudo.
	_, err := chapters.Create(ctx, &domain.Chapter{
		BookID: book.ID, Title: "Repetido", Position: 1, Status: domain.ChapterStatusDraft,
	})

	if !errors.Is(err, ErrPositionTaken) {
		t.Fatalf("error = %v, se esperaba ErrPositionTaken", err)
	}
}

func TestReorder_NoChocaConElUniqueDePosicion(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	books := NewPostgresBookRepository(pool)
	chapters := NewPostgresChapterRepository(pool)
	author := newAuthor(t, pool)

	book := newBook(t, books, author, "Para reordenar", domain.BookStatusDraft)
	ids := []string{}
	inicial, _ := chapters.ListByBook(ctx, book.ID, false)
	ids = append(ids, inicial[0].ID)
	for range 2 {
		ch, err := chapters.Create(ctx, &domain.Chapter{
			BookID: book.ID, Title: "Otro", Status: domain.ChapterStatusDraft,
		})
		if err != nil {
			t.Fatalf("error inesperado: %v", err)
		}
		ids = append(ids, ch.ID)
	}

	// Invertir el orden es el caso que rompe si se escriben las posiciones de
	// una sola pasada: la primera fila chocaria con la que aun no se movio.
	invertido := []string{ids[2], ids[1], ids[0]}
	got, err := chapters.Reorder(ctx, book.ID, invertido)
	if err != nil {
		t.Fatalf("el reorder fallo: %v", err)
	}

	if len(got) != 3 {
		t.Fatalf("capitulos = %d, se esperaban 3", len(got))
	}
	for i, ch := range got {
		if ch.Position != int32(i+1) {
			t.Fatalf("position[%d] = %d, se esperaba %d", i, ch.Position, i+1)
		}
		if ch.ID != invertido[i] {
			t.Fatalf("orden incorrecto en la posicion %d", i+1)
		}
	}
}

func TestReorder_RechazaConjuntoQueNoCoincide(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	books := NewPostgresBookRepository(pool)
	chapters := NewPostgresChapterRepository(pool)
	author := newAuthor(t, pool)

	book := newBook(t, books, author, "Incompleto", domain.BookStatusDraft)
	otro := newBook(t, books, author, "Ajeno", domain.BookStatusDraft)
	suyos, _ := chapters.ListByBook(ctx, book.ID, false)
	ajenos, _ := chapters.ListByBook(ctx, otro.ID, false)

	// Un capitulo de otro libro: el conteo cuadra, pero no pertenece.
	_, err := chapters.Reorder(ctx, book.ID, []string{ajenos[0].ID})
	if !errors.Is(err, ErrReorderMismatch) {
		t.Fatalf("error = %v, se esperaba ErrReorderMismatch", err)
	}

	// Y el capitulo propio no debe haberse movido.
	tras, _ := chapters.ListByBook(ctx, book.ID, false)
	if tras[0].Position != suyos[0].Position {
		t.Fatalf("la transaccion no revirtio: position = %d", tras[0].Position)
	}
}

func TestGetByID_UUIDMalformadoEsNotFound(t *testing.T) {
	pool := testPool(t)
	books := NewPostgresBookRepository(pool)

	// Postgres responde 22P02 (invalid_text_representation) y el repositorio
	// lo traduce: para una busqueda puntual, malformado y inexistente son lo
	// mismo.
	_, err := books.GetByID(context.Background(), "no-soy-un-uuid")

	if !errors.Is(err, ErrBookNotFound) {
		t.Fatalf("error = %v, se esperaba ErrBookNotFound", err)
	}
}

func TestSavedBooks_UniquePorCategoriaYCoexistencia(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	books := NewPostgresBookRepository(pool)
	saved := NewPostgresSavedBookRepository(pool)
	author := newAuthor(t, pool)

	book := newBook(t, books, author, "Guardable", domain.BookStatusPublished)

	if _, err := saved.Save(ctx, author, book.ID, domain.SavedKindFavorite); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	// Repetir la misma categoria choca con el UNIQUE.
	_, err := saved.Save(ctx, author, book.ID, domain.SavedKindFavorite)
	if !errors.Is(err, ErrAlreadySaved) {
		t.Fatalf("error = %v, se esperaba ErrAlreadySaved", err)
	}

	// Pero favorite y read_later son filas distintas y pueden coexistir.
	if _, err := saved.Save(ctx, author, book.ID, domain.SavedKindReadLater); err != nil {
		t.Fatalf("favorite y read_later deberian coexistir: %v", err)
	}

	todos, err := saved.ListByUser(ctx, author, "")
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if len(todos) != 2 {
		t.Fatalf("entradas = %d, se esperaban 2", len(todos))
	}
	// El JOIN cruza de reader a content: la entrada trae los datos del libro.
	if todos[0].Book == nil || todos[0].Book.Title != "Guardable" {
		t.Fatal("el JOIN con content.books no trajo los datos del libro")
	}
}

func TestSavedBooks_UnsaveSinKindQuitaDeAmbas(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	books := NewPostgresBookRepository(pool)
	saved := NewPostgresSavedBookRepository(pool)
	author := newAuthor(t, pool)

	book := newBook(t, books, author, "Para quitar", domain.BookStatusPublished)
	saved.Save(ctx, author, book.ID, domain.SavedKindFavorite)
	saved.Save(ctx, author, book.ID, domain.SavedKindReadLater)

	if err := saved.Unsave(ctx, author, book.ID, ""); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	quedan, _ := saved.ListByUser(ctx, author, "")
	if len(quedan) != 0 {
		t.Fatalf("quedaron %d entradas", len(quedan))
	}
}

func TestListBooks_FiltraPaginaYCuentaElTotal(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	books := NewPostgresBookRepository(pool)
	author := newAuthor(t, pool)

	newBook(t, books, author, "Publicado uno", domain.BookStatusPublished)
	newBook(t, books, author, "Publicado dos", domain.BookStatusPublished)
	newBook(t, books, author, "Borrador", domain.BookStatusDraft)

	got, total, err := books.List(ctx, domain.BookFilter{
		Page: 1, PageSize: 1, AuthorID: author, Status: domain.BookStatusPublished,
	})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	// total es cuantos cumplen el filtro, no cuantos trae la pagina.
	if total != 2 {
		t.Fatalf("total = %d, se esperaba 2", total)
	}
	if len(got) != 1 {
		t.Fatalf("la pagina trajo %d libros, se esperaba 1", len(got))
	}
}

func TestSagaBooks_VinculaOrdenaYDesvincula(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	books := NewPostgresBookRepository(pool)
	sagas := NewPostgresSagaRepository(pool)
	author := newAuthor(t, pool)

	saga, err := sagas.Create(ctx, &domain.Saga{AuthorID: author, Title: "Trilogia"})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	uno := newBook(t, books, author, "Primero", domain.BookStatusPublished)
	dos := newBook(t, books, author, "Segundo", domain.BookStatusPublished)

	if err := sagas.AddBook(ctx, saga.ID, uno.ID, 0); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if err := sagas.AddBook(ctx, saga.ID, dos.ID, 0); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	// Repetir el mismo libro choca con la PK compuesta.
	if err := sagas.AddBook(ctx, saga.ID, uno.ID, 0); !errors.Is(err, ErrBookAlreadyInSaga) {
		t.Fatalf("error = %v, se esperaba ErrBookAlreadyInSaga", err)
	}

	if err := sagas.ReorderBooks(ctx, saga.ID, []string{dos.ID, uno.ID}); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	ordenados, err := sagas.ListBooks(ctx, saga.ID)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if len(ordenados) != 2 || ordenados[0].ID != dos.ID {
		t.Fatal("el reorder de la saga no aplico")
	}

	if err := sagas.RemoveBook(ctx, saga.ID, dos.ID); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	quedan, _ := sagas.ListBooks(ctx, saga.ID)
	if len(quedan) != 1 {
		t.Fatalf("quedan %d libros en la saga, se esperaba 1", len(quedan))
	}
}

func TestDeleteSaga_NoBorraLosLibros(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	books := NewPostgresBookRepository(pool)
	sagas := NewPostgresSagaRepository(pool)
	author := newAuthor(t, pool)

	saga, _ := sagas.Create(ctx, &domain.Saga{AuthorID: author, Title: "Efimera"})
	book := newBook(t, books, author, "Sobreviviente", domain.BookStatusPublished)
	sagas.AddBook(ctx, saga.ID, book.ID, 0)

	if err := sagas.Delete(ctx, saga.ID); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	// Pertenecer a una saga es opcional: el libro sigue existiendo.
	if _, err := books.GetByID(ctx, book.ID); err != nil {
		t.Fatalf("el libro no deberia haberse borrado con la saga: %v", err)
	}
}
