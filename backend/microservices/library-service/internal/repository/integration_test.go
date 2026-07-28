package repository

import (
	"context"
	"errors"
	"fmt"
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
		// Los capitulos, los generos y los vinculos de saga se van por CASCADE
		// al borrar el libro.
		pool.Exec(ctx, `DELETE FROM content.books WHERE author_id = $1`, id)
		pool.Exec(ctx, `DELETE FROM content.sagas WHERE author_id = $1`, id)
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
	book, err := repo.Create(ctx, &domain.Book{AuthorID: authorID, Title: title, Status: bookStatus}, nil)
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
	}, nil)
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
	}, nil)
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

func TestDeleteBook_ArrastraCapitulosYGeneros(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	books := NewPostgresBookRepository(pool)
	genres := NewPostgresGenreRepository(pool)
	author := newAuthor(t, pool)

	book := newBook(t, books, author, "Para borrar", domain.BookStatusPublished)
	if err := genres.ReplaceForBook(ctx, book.ID, []string{"fantasy", "horror"}); err != nil {
		t.Fatalf("no se pudieron poner los generos: %v", err)
	}

	if err := books.Delete(ctx, book.ID); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	for _, q := range []struct {
		nombre string
		query  string
	}{
		{"capitulos", `SELECT count(*) FROM content.chapters WHERE book_id = $1`},
		{"generos", `SELECT count(*) FROM content.book_genres WHERE book_id = $1`},
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

// El tope de 4 generos por libro lo sostiene un trigger, que es justamente lo
// que un doble en memoria no puede demostrar.
func TestBookGenres_ElTopeLoImponeLaBase(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	books := NewPostgresBookRepository(pool)
	genres := NewPostgresGenreRepository(pool)
	author := newAuthor(t, pool)

	book := newBook(t, books, author, "Con generos", domain.BookStatusPublished)

	// Cuatro entran; el quinto no. Se llama al repositorio directo, saltandose
	// la validacion del servicio: lo que se prueba es la red de abajo.
	cuatro := []string{"fantasy", "horror", "romance", "drama"}
	if err := genres.ReplaceForBook(ctx, book.ID, cuatro); err != nil {
		t.Fatalf("cuatro generos deberian entrar: %v", err)
	}

	err := genres.ReplaceForBook(ctx, book.ID, append(cuatro, "comedy"))
	if !errors.Is(err, ErrTooManyGenres) {
		t.Fatalf("error = %v, se esperaba ErrTooManyGenres", err)
	}

	// Y la transaccion que fallo no dejo nada a medias: siguen los cuatro.
	quedan, err := books.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if len(quedan.Genres) != domain.GenreMaxPerBook {
		t.Fatalf("generos = %d, se esperaban %d", len(quedan.Genres), domain.GenreMaxPerBook)
	}
}

func TestBookGenres_UnSlugFueraDelCatalogoNoSeEscribe(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	books := NewPostgresBookRepository(pool)
	genres := NewPostgresGenreRepository(pool)
	author := newAuthor(t, pool)

	book := newBook(t, books, author, "Con genero raro", domain.BookStatusPublished)
	if err := genres.ReplaceForBook(ctx, book.ID, []string{"fantasy"}); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	// La FK contra content.genres es lo que traduce el slug inventado.
	err := genres.ReplaceForBook(ctx, book.ID, []string{"fantasy", "genero-inventado"})
	if !errors.Is(err, ErrGenreNotFound) {
		t.Fatalf("error = %v, se esperaba ErrGenreNotFound", err)
	}

	// El DELETE previo al INSERT no se puede haber quedado aplicado solo.
	quedan, err := books.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if len(quedan.Genres) != 1 {
		t.Fatalf("generos = %d, se esperaba 1: la escritura fallida no debe borrar los que habia",
			len(quedan.Genres))
	}
}

// Create escribe libro y generos en la misma transaccion: si el genero no
// existe, no puede quedar el libro creado.
func TestCreateBook_ConGeneroInvalidoNoDejaLibro(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	books := NewPostgresBookRepository(pool)
	author := newAuthor(t, pool)

	_, err := books.Create(ctx, &domain.Book{
		AuthorID: author, Title: "No deberia existir", Status: domain.BookStatusDraft,
	}, []string{"genero-inventado"})
	if !errors.Is(err, ErrGenreNotFound) {
		t.Fatalf("error = %v, se esperaba ErrGenreNotFound", err)
	}

	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM content.books WHERE author_id = $1`, author).Scan(&n); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if n != 0 {
		t.Fatalf("quedaron %d libros de un alta que fallo", n)
	}
}

// El filtro por genero no puede duplicar libros ni inflar el total, que es lo
// que pasaria con un JOIN en vez del EXISTS.
func TestListBooks_FiltraPorGeneroSinDuplicar(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	books := NewPostgresBookRepository(pool)
	author := newAuthor(t, pool)

	conGeneros, err := books.Create(ctx, &domain.Book{
		AuthorID: author, Title: "Con varios", Status: domain.BookStatusPublished,
	}, []string{"fantasy", "horror", "drama"})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if _, err := books.Create(ctx, &domain.Book{
		AuthorID: author, Title: "Sin generos", Status: domain.BookStatusPublished,
	}, nil); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	pagina, total, err := books.List(ctx, domain.BookFilter{
		Page: 1, PageSize: 10, AuthorID: author, Genre: "fantasy",
	})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if total != 1 || len(pagina) != 1 {
		t.Fatalf("total = %d con %d libros; se esperaba 1 y 1", total, len(pagina))
	}
	if pagina[0].ID != conGeneros.ID {
		t.Fatalf("el filtro devolvio el libro equivocado")
	}
	// Y el libro viene con sus generos resueltos a nombre, no solo el filtrado.
	if len(pagina[0].Genres) != 3 || pagina[0].Genres[0].Name == "" {
		t.Fatalf("generos = %v, se esperaban los 3 con su nombre", pagina[0].Genres)
	}

	// Un slug que no existe no es un error: simplemente no tiene libros.
	_, ninguno, err := books.List(ctx, domain.BookFilter{
		Page: 1, PageSize: 10, AuthorID: author, Genre: "genero-inventado",
	})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if ninguno != 0 {
		t.Fatalf("total = %d, se esperaba 0", ninguno)
	}
}

// PageSize en 0 saca la consulta sin LIMIT ni OFFSET, que es lo que sostiene
// que el listado de los libros propios no se pagine. Solo se puede comprobar
// contra el SQL real: el doble en memoria lo imita, pero no lo demuestra.
func TestListBooks_SinPageSizeDevuelveTodo(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	books := NewPostgresBookRepository(pool)
	author := newAuthor(t, pool)

	const cuantos = 25
	for i := 0; i < cuantos; i++ {
		if _, err := books.Create(ctx, &domain.Book{
			AuthorID: author,
			Title:    fmt.Sprintf("Libro %02d", i),
			Status:   domain.BookStatusDraft,
		}, nil); err != nil {
			t.Fatalf("error inesperado: %v", err)
		}
	}

	todos, total, err := books.List(ctx, domain.BookFilter{AuthorID: author})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if len(todos) != cuantos || total != cuantos {
		t.Fatalf("libros = %d con total = %d; se esperaban los %d sin paginar",
			len(todos), total, cuantos)
	}

	// Con un tamano de pagina si recorta, y el total sigue siendo el del filtro.
	pagina, total, err := books.List(ctx, domain.BookFilter{
		Page: 1, PageSize: 10, AuthorID: author,
	})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if len(pagina) != 10 || total != cuantos {
		t.Fatalf("pagina = %d con total = %d; se esperaban 10 y %d", len(pagina), total, cuantos)
	}
}

func TestListGenres_TraeElCatalogoDeLaMigracion(t *testing.T) {
	pool := testPool(t)
	genres := NewPostgresGenreRepository(pool)

	catalogo, err := genres.List(context.Background())
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if len(catalogo) == 0 {
		t.Fatal("el catalogo esta vacio; la migracion de generos no se aplico")
	}
	for _, g := range catalogo {
		if g.Slug == "" || g.Name == "" {
			t.Fatalf("genero incompleto: %+v", g)
		}
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

// La baja de cuenta se apoya en los CASCADE del esquema, que es justo lo que un
// doble en memoria no puede demostrar.
func TestDeleteByAuthor_ArrastraTodoLoDelUsuario(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	books := NewPostgresBookRepository(pool)
	sagas := NewPostgresSagaRepository(pool)
	genres := NewPostgresGenreRepository(pool)
	autor := newAuthor(t, pool)
	otro := newAuthor(t, pool)

	// Del autor: dos libros (con sus capitulos y sus generos) y una saga con
	// uno de ellos dentro.
	uno := newBook(t, books, autor, "Suyo uno", domain.BookStatusPublished)
	dos := newBook(t, books, autor, "Suyo dos", domain.BookStatusDraft)
	saga, err := sagas.Create(ctx, &domain.Saga{AuthorID: autor, Title: "Su saga"})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if err := sagas.AddBook(ctx, saga.ID, uno.ID, 0); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if err := genres.ReplaceForBook(ctx, uno.ID, []string{"fantasy", "horror"}); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	delOtro := newBook(t, books, otro, "Ajeno", domain.BookStatusPublished)

	nLibros, nSagas, err := books.DeleteByAuthor(ctx, autor)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if nLibros != 2 || nSagas != 1 {
		t.Fatalf("borrados = %d libros, %d sagas; se esperaba 2 y 1", nLibros, nSagas)
	}

	// Nada suyo sobrevive, ni lo que colgaba de sus libros.
	for _, q := range []struct {
		nombre string
		query  string
		arg    any
	}{
		{"libros", `SELECT count(*) FROM content.books WHERE author_id = $1`, autor},
		{"sagas", `SELECT count(*) FROM content.sagas WHERE author_id = $1`, autor},
		{"capitulos de sus libros", `SELECT count(*) FROM content.chapters WHERE book_id = $1`, uno.ID},
		{"generos de sus libros", `SELECT count(*) FROM content.book_genres WHERE book_id = $1`, uno.ID},
		{"vinculos de su saga", `SELECT count(*) FROM content.saga_books WHERE saga_id = $1`, saga.ID},
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
	if _, err := books.GetByID(ctx, delOtro.ID); err != nil {
		t.Fatalf("se borro el libro de otro autor: %v", err)
	}
	if _, err := books.GetByID(ctx, dos.ID); err == nil {
		t.Fatal("el segundo libro del autor deberia haberse borrado")
	}
}

func TestDeleteByAuthor_EsIdempotente(t *testing.T) {
	pool := testPool(t)
	books := NewPostgresBookRepository(pool)
	autor := newAuthor(t, pool)

	// Sobre un usuario sin nada no falla: devuelve ceros. Es lo que permite que
	// user-service reintente la baja si el borrado de la cuenta fallo despues.
	nLibros, nSagas, err := books.DeleteByAuthor(context.Background(), autor)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if nLibros != 0 || nSagas != 0 {
		t.Fatalf("borrados = %d libros y %d sagas; se esperaban ceros", nLibros, nSagas)
	}
}

// La subconsulta del conteo es lo unico de chapter_count que los dobles no
// pueden probar: aqui se comprueba contra el SQL real, en las cuatro consultas
// que devuelven libros.
//
// La regla es una sola y no depende de quien pregunte: cuenta los publicados.
// Un borrador todavia no es parte del libro.
func TestChapterCount_SoloCuentaLosPublicados(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	books := NewPostgresBookRepository(pool)
	chapters := NewPostgresChapterRepository(pool)
	sagas := NewPostgresSagaRepository(pool)
	author := newAuthor(t, pool)

	// newBook ya deja un capitulo publicado; se agregan dos en borrador.
	book := newBook(t, books, author, "Con borradores", domain.BookStatusPublished)
	for _, titulo := range []string{"Inedito uno", "Inedito dos"} {
		if _, err := chapters.Create(ctx, &domain.Chapter{
			BookID: book.ID, Title: titulo, Status: domain.ChapterStatusDraft,
		}); err != nil {
			t.Fatalf("error inesperado: %v", err)
		}
	}

	// Tres filas en la tabla, una sola publicada: el conteo tiene que decir 1.
	t.Run("GetByID", func(t *testing.T) {
		b, err := books.GetByID(ctx, book.ID)
		if err != nil {
			t.Fatalf("error inesperado: %v", err)
		}
		if b.ChapterCount != 1 {
			t.Fatalf("chapterCount = %d, se esperaba 1 (hay 3 capitulos, 1 publicado)", b.ChapterCount)
		}
	})

	t.Run("List", func(t *testing.T) {
		pagina, _, err := books.List(ctx, domain.BookFilter{Page: 1, PageSize: 10, AuthorID: author})
		if err != nil {
			t.Fatalf("error inesperado: %v", err)
		}
		if len(pagina) != 1 || pagina[0].ChapterCount != 1 {
			t.Fatalf("en el listado el conteo deberia ser 1")
		}
	})

	// Update lleva su propia copia de la subconsulta, asi que se comprueba
	// aparte: es donde mas facil se cuela un conteo con otra regla.
	t.Run("Update", func(t *testing.T) {
		titulo := "Con borradores (revisado)"
		b, err := books.Update(ctx, &domain.BookUpdate{ID: book.ID, Title: &titulo})
		if err != nil {
			t.Fatalf("error inesperado: %v", err)
		}
		if b.ChapterCount != 1 {
			t.Fatalf("tras Update el conteo deberia ser 1, fue %d", b.ChapterCount)
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

		enSaga, err := sagas.ListBooks(ctx, saga.ID)
		if err != nil {
			t.Fatalf("error inesperado: %v", err)
		}
		if len(enSaga) != 1 || enSaga[0].ChapterCount != 1 {
			t.Fatalf("en la saga el conteo deberia ser 1")
		}
	})

	// Y al publicar uno de los borradores, el conteo sube.
	t.Run("al publicar un borrador sube", func(t *testing.T) {
		lista, _ := chapters.ListByBook(ctx, book.ID, false)
		var borrador string
		for _, ch := range lista {
			if ch.Status == domain.ChapterStatusDraft {
				borrador = ch.ID
				break
			}
		}
		publicado := domain.ChapterStatusPublished
		if _, err := chapters.Update(ctx, &domain.ChapterUpdate{
			ID: borrador, BookID: book.ID, Status: &publicado,
		}); err != nil {
			t.Fatalf("error inesperado: %v", err)
		}

		b, err := books.GetByID(ctx, book.ID)
		if err != nil {
			t.Fatalf("error inesperado: %v", err)
		}
		if b.ChapterCount != 2 {
			t.Fatalf("chapterCount = %d, se esperaban 2", b.ChapterCount)
		}
	})
}

// El COALESCE que fija published_at vive en el SQL, asi que es aqui donde se
// puede demostrar: se escribe la primera vez y ninguna transicion posterior lo
// mueve.
func TestPublishedAt_LoFijaElUpdateUnaSolaVez(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	books := NewPostgresBookRepository(pool)
	chapters := NewPostgresChapterRepository(pool)
	author := newAuthor(t, pool)

	book := newBook(t, books, author, "Con fecha", domain.BookStatusDraft)
	if book.PublishedAt != nil {
		t.Fatal("un libro creado en borrador no deberia traer published_at")
	}

	publicado := domain.BookStatusPublished
	borrador := domain.BookStatusDraft

	primera, err := books.Update(ctx, &domain.BookUpdate{ID: book.ID, Status: &publicado})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if primera.PublishedAt == nil {
		t.Fatal("al publicar deberia quedar la fecha")
	}
	original := *primera.PublishedAt

	// Despublicar la conserva.
	tras, err := books.Update(ctx, &domain.BookUpdate{ID: book.ID, Status: &borrador})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if tras.PublishedAt == nil || !tras.PublishedAt.Equal(original) {
		t.Fatalf("despublicar movio published_at: %v -> %v", original, tras.PublishedAt)
	}

	// Y republicar tampoco la pisa, que es lo que impide colarse de nuevo en
	// las novedades.
	otra, err := books.Update(ctx, &domain.BookUpdate{ID: book.ID, Status: &publicado})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if !otra.PublishedAt.Equal(original) {
		t.Fatalf("republicar piso published_at: %v -> %v", original, otra.PublishedAt)
	}

	// Un capitulo que nace publicado la trae del INSERT, no del UPDATE.
	nacePublicado, err := chapters.Create(ctx, &domain.Chapter{
		BookID: book.ID, Title: "Nace publicado", Status: domain.ChapterStatusPublished,
	})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if nacePublicado.PublishedAt == nil {
		t.Fatal("un capitulo creado publicado deberia traer published_at")
	}

	// Y uno que nace en borrador, no.
	naceBorrador, err := chapters.Create(ctx, &domain.Chapter{
		BookID: book.ID, Title: "Nace borrador", Status: domain.ChapterStatusDraft,
	})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if naceBorrador.PublishedAt != nil {
		t.Fatal("un capitulo en borrador no deberia traer published_at")
	}
}
