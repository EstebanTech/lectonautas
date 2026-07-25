-- Esquema content: lo que escribe el autor (libros, capitulos, sagas).
CREATE SCHEMA IF NOT EXISTS content;

CREATE TABLE IF NOT EXISTS content.books (
    -- author_id es el user_id de user-service, que vive en otra base de datos:
    -- por eso es UUID plano y SIN foreign key. Su validez se comprueba en la
    -- aplicacion (gRPC contra user-service), nunca con una FK.
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    author_id   UUID         NOT NULL,
    title       VARCHAR(255) NOT NULL,
    description TEXT,
    cover_url   VARCHAR(500),
    status      VARCHAR(20)  NOT NULL DEFAULT 'draft'
                CHECK (status IN ('draft', 'published', 'archived')),
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS books_author_id_idx ON content.books (author_id);
-- Los listados publicos filtran por status = 'published' y ordenan por fecha.
CREATE INDEX IF NOT EXISTS books_status_created_at_idx ON content.books (status, created_at DESC);

-- Todo el contenido de un libro vive aqui: books no tiene columna de texto.
-- Un libro de un solo capitulo es simplemente una fila en esta tabla.
CREATE TABLE IF NOT EXISTS content.chapters (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    book_id    UUID         NOT NULL REFERENCES content.books(id) ON DELETE CASCADE,
    title      VARCHAR(255) NOT NULL,
    content    TEXT,
    position   INT          NOT NULL,
    status     VARCHAR(20)  NOT NULL DEFAULT 'draft'
               CHECK (status IN ('draft', 'published')),
    created_at TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ  NOT NULL DEFAULT now(),
    -- No puede haber dos capitulos en la misma posicion dentro de un libro.
    CONSTRAINT chapters_book_id_position_key UNIQUE (book_id, position)
);

CREATE TABLE IF NOT EXISTS content.sagas (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    author_id   UUID         NOT NULL,
    title       VARCHAR(255) NOT NULL,
    description TEXT,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS sagas_author_id_idx ON content.sagas (author_id);

-- Tabla puente saga<->libro. No se expone como recurso propio: vincular un
-- libro es una accion sobre la saga. Sin fila aqui, el libro no pertenece a
-- ninguna saga.
CREATE TABLE IF NOT EXISTS content.saga_books (
    saga_id  UUID NOT NULL REFERENCES content.sagas(id) ON DELETE CASCADE,
    book_id  UUID NOT NULL REFERENCES content.books(id) ON DELETE CASCADE,
    position INT  NOT NULL,
    -- Un libro no se repite dentro de la misma saga.
    PRIMARY KEY (saga_id, book_id)
);

CREATE INDEX IF NOT EXISTS saga_books_book_id_idx ON content.saga_books (book_id);
