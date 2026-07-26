package repository

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EstebanTech/lectonautas/backend/microservices/library-service/internal/domain"
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

// newBook crea el libro y le agrega un primer capitulo por el camino normal
// (dos operaciones, como las hace un cliente): el libro ya no nace con
// contenido, pero casi todas las pruebas de abajo necesitan tenerlo.
//
// El capitulo sigue el estado del libro para no dejar datos que contradigan la
// invariante del servicio: un libro publicado tiene al menos uno publicado.
func newBook(t *testing.T, repo *PostgresBookRepository, authorID, title, bookStatus string) *domain.Book {
	t.Helper()

	ctx := context.Background()
	book, err := repo.Create(ctx, &domain.Book{AuthorID: authorID, Title: title, Status: bookStatus})
	if err != nil {
		t.Fatalf("no se pudo crear el libro: %v", err)
	}

	chapterStatus := domain.ChapterStatusDraft
	if bookStatus == domain.BookStatusPublished {
		chapterStatus = domain.ChapterStatusPublished
	}
	chapters := NewPostgresChapterRepository(repo.pool)
	if _, err := chapters.Create(ctx, &domain.Chapter{
		BookID: book.ID, Title: "Capitulo 1", Status: chapterStatus,
	}); err != nil {
		t.Fatalf("no se pudo crear el capitulo inicial: %v", err)
	}
	return book
}

func TestCreateBook_NaceVacio(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	books := NewPostgresBookRepository(pool)
	chapters := NewPostgresChapterRepository(pool)
	author := newAuthor(t, pool)

	book, err := books.Create(ctx, &domain.Book{
		AuthorID: author, Title: "Sin capitulos", Status: domain.BookStatusDraft,
	})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	// Que el esquema acepte un libro sin capitulos es la premisa del diseno:
	// si hubiera una restriccion que lo impidiera, esto fallaria aqui.
	got, err := chapters.ListByBook(ctx, book.ID, false)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("capitulos = %d, se esperaba ninguno", len(got))
	}
}

func TestCreateChapter_ElPrimeroTomaLaPosicionUno(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	books := NewPostgresBookRepository(pool)
	chapters := NewPostgresChapterRepository(pool)
	author := newAuthor(t, pool)

	book, err := books.Create(ctx, &domain.Book{
		AuthorID: author, Title: "Recien creado", Status: domain.BookStatusDraft,
	})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	// Sobre un libro vacio, el COALESCE(max(position), 0) + 1 tiene que dar 1.
	ch, err := chapters.Create(ctx, &domain.Chapter{
		BookID: book.ID, Title: "El embarque", Position: 0, Status: domain.ChapterStatusDraft,
	})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if ch.Position != 1 {
		t.Fatalf("position del primer capitulo = %d, se esperaba 1", ch.Position)
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
	_, err := books.GetByID(context.Background(), "no-soy-un-uuid", "")

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
	ordenados, err := sagas.ListBooks(ctx, saga.ID, author)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if len(ordenados) != 2 || ordenados[0].ID != dos.ID {
		t.Fatal("el reorder de la saga no aplico")
	}

	if err := sagas.RemoveBook(ctx, saga.ID, dos.ID); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	quedan, _ := sagas.ListBooks(ctx, saga.ID, author)
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
	if _, err := books.GetByID(ctx, book.ID, author); err != nil {
		t.Fatalf("el libro no deberia haberse borrado con la saga: %v", err)
	}
}

// La baja de cuenta se apoya en los CASCADE del esquema, que es justo lo que un
// doble en memoria no puede demostrar.
func TestDeleteByAuthor_ArrastraTodoLoDelUsuario(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	books := NewPostgresBookRepository(pool)
	sagas := NewPostgresSagaRepository(pool)
	saved := NewPostgresSavedBookRepository(pool)
	autor := newAuthor(t, pool)
	otro := newAuthor(t, pool)

	// Del autor: dos libros (con sus capitulos), una saga con uno de ellos
	// dentro, y un libro ajeno guardado en su biblioteca.
	uno := newBook(t, books, autor, "Suyo uno", domain.BookStatusPublished)
	dos := newBook(t, books, autor, "Suyo dos", domain.BookStatusDraft)
	saga, err := sagas.Create(ctx, &domain.Saga{AuthorID: autor, Title: "Su saga"})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if err := sagas.AddBook(ctx, saga.ID, uno.ID, 0); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	delOtro := newBook(t, books, otro, "Ajeno", domain.BookStatusPublished)
	if _, err := saved.Save(ctx, autor, delOtro.ID, domain.SavedKindFavorite); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	// Y un tercero que tenia guardado un libro del autor que se da de baja: esa
	// fila tiene que irse tambien, por CASCADE del libro.
	if _, err := saved.Save(ctx, otro, uno.ID, domain.SavedKindReadLater); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	nLibros, nSagas, nGuardados, err := books.DeleteByAuthor(ctx, autor)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if nLibros != 2 || nSagas != 1 || nGuardados != 1 {
		t.Fatalf("borrados = %d libros, %d sagas, %d guardados; se esperaba 2, 1, 1",
			nLibros, nSagas, nGuardados)
	}

	// Nada suyo sobrevive, ni lo que colgaba de sus libros.
	for _, q := range []struct {
		nombre string
		query  string
		arg    any
	}{
		{"libros", `SELECT count(*) FROM content.books WHERE author_id = $1`, autor},
		{"sagas", `SELECT count(*) FROM content.sagas WHERE author_id = $1`, autor},
		{"su biblioteca", `SELECT count(*) FROM reader.saved_books WHERE user_id = $1`, autor},
		{"capitulos de sus libros", `SELECT count(*) FROM content.chapters WHERE book_id = $1`, uno.ID},
		{"vinculos de su saga", `SELECT count(*) FROM content.saga_books WHERE saga_id = $1`, saga.ID},
		{"su libro guardado por terceros", `SELECT count(*) FROM reader.saved_books WHERE book_id = $1`, uno.ID},
	} {
		var n int
		if err := pool.QueryRow(ctx, q.query, q.arg).Scan(&n); err != nil {
			t.Fatalf("error inesperado: %v", err)
		}
		if n != 0 {
			t.Fatalf("quedaron %d %s", n, q.nombre)
		}
	}

	// Y lo del otro autor sigue intacto.
	if _, err := books.GetByID(ctx, delOtro.ID, otro); err != nil {
		t.Fatalf("se borro el libro de otro autor: %v", err)
	}
	if _, err := books.GetByID(ctx, dos.ID, autor); err == nil {
		t.Fatal("el segundo libro del autor deberia haberse borrado")
	}
}

func TestDeleteByAuthor_EsIdempotente(t *testing.T) {
	pool := testPool(t)
	books := NewPostgresBookRepository(pool)
	autor := newAuthor(t, pool)

	// Sobre un usuario sin nada no falla: devuelve ceros. Es lo que permite que
	// user-service reintente la baja si el borrado de la cuenta fallo despues.
	nLibros, nSagas, nGuardados, err := books.DeleteByAuthor(context.Background(), autor)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if nLibros != 0 || nSagas != 0 || nGuardados != 0 {
		t.Fatalf("borrados = %d, %d, %d; se esperaban ceros", nLibros, nSagas, nGuardados)
	}
}

// La subconsulta del conteo es lo unico de chapter_count que los dobles no
// pueden probar: aqui se comprueba contra el SQL real, en las cuatro consultas
// que devuelven libros.
func TestChapterCount_CuentaSegunQuienPregunta(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	books := NewPostgresBookRepository(pool)
	chapters := NewPostgresChapterRepository(pool)
	sagas := NewPostgresSagaRepository(pool)
	saved := NewPostgresSavedBookRepository(pool)
	author := newAuthor(t, pool)
	otro := newAuthor(t, pool)

	// newBook ya deja un capitulo publicado; se agrega uno en borrador.
	book := newBook(t, books, author, "Con borrador", domain.BookStatusPublished)
	if _, err := chapters.Create(ctx, &domain.Chapter{
		BookID: book.ID, Title: "Inedito", Status: domain.ChapterStatusDraft,
	}); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	t.Run("GetByID", func(t *testing.T) {
		propio, err := books.GetByID(ctx, book.ID, author)
		if err != nil {
			t.Fatalf("error inesperado: %v", err)
		}
		if propio.ChapterCount != 2 {
			t.Fatalf("el autor deberia contar 2, conto %d", propio.ChapterCount)
		}

		ajeno, err := books.GetByID(ctx, book.ID, otro)
		if err != nil {
			t.Fatalf("error inesperado: %v", err)
		}
		if ajeno.ChapterCount != 1 {
			t.Fatalf("un lector ajeno deberia contar 1, conto %d", ajeno.ChapterCount)
		}

		// El anonimo llega con cadena vacia, que no es un uuid: el NULLIF de la
		// consulta es lo que evita que Postgres reviente por el cast.
		anonimo, err := books.GetByID(ctx, book.ID, "")
		if err != nil {
			t.Fatalf("error inesperado: %v", err)
		}
		if anonimo.ChapterCount != 1 {
			t.Fatalf("un anonimo deberia contar 1, conto %d", anonimo.ChapterCount)
		}
	})

	t.Run("List", func(t *testing.T) {
		propios, _, err := books.List(ctx, domain.BookFilter{
			Page: 1, PageSize: 10, AuthorID: author, ViewerID: author,
		})
		if err != nil {
			t.Fatalf("error inesperado: %v", err)
		}
		if len(propios) != 1 || propios[0].ChapterCount != 2 {
			t.Fatalf("en su listado el autor deberia contar 2")
		}

		publicos, _, err := books.List(ctx, domain.BookFilter{
			Page: 1, PageSize: 10, AuthorID: author,
		})
		if err != nil {
			t.Fatalf("error inesperado: %v", err)
		}
		if len(publicos) != 1 || publicos[0].ChapterCount != 1 {
			t.Fatalf("sin viewer el listado deberia contar 1")
		}
	})

	t.Run("ListBooks de la saga", func(t *testing.T) {
		saga, err := sagas.Create(ctx, &domain.Saga{AuthorID: author, Title: "Con conteo"})
		if err != nil {
			t.Fatalf("error inesperado: %v", err)
		}
		if err := sagas.AddBook(ctx, saga.ID, book.ID, 0); err != nil {
			t.Fatalf("error inesperado: %v", err)
		}

		propios, err := sagas.ListBooks(ctx, saga.ID, author)
		if err != nil {
			t.Fatalf("error inesperado: %v", err)
		}
		if len(propios) != 1 || propios[0].ChapterCount != 2 {
			t.Fatalf("el autor deberia contar 2 en su saga")
		}

		ajenos, err := sagas.ListBooks(ctx, saga.ID, otro)
		if err != nil {
			t.Fatalf("error inesperado: %v", err)
		}
		if len(ajenos) != 1 || ajenos[0].ChapterCount != 1 {
			t.Fatalf("un lector ajeno deberia contar 1 en esa saga")
		}
	})

	t.Run("biblioteca del lector", func(t *testing.T) {
		// El lector guarda un libro ajeno: cuenta solo lo publicado.
		if _, err := saved.Save(ctx, otro, book.ID, domain.SavedKindFavorite); err != nil {
			t.Fatalf("error inesperado: %v", err)
		}
		lista, err := saved.ListByUser(ctx, otro, "")
		if err != nil {
			t.Fatalf("error inesperado: %v", err)
		}
		if len(lista) != 1 || lista[0].Book.ChapterCount != 1 {
			t.Fatalf("en la biblioteca de un ajeno el conteo deberia ser 1")
		}

		// El autor guarda el suyo: ahi si cuenta el borrador.
		propio, err := saved.Save(ctx, author, book.ID, domain.SavedKindFavorite)
		if err != nil {
			t.Fatalf("error inesperado: %v", err)
		}
		if propio.Book.ChapterCount != 2 {
			t.Fatalf("al guardar su propio libro el autor deberia contar 2, conto %d",
				propio.Book.ChapterCount)
		}
	})
}
