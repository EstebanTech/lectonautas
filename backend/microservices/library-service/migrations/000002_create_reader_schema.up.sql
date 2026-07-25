-- Esquema reader: la biblioteca del lector, separada de lo que escribe el autor.
CREATE SCHEMA IF NOT EXISTS reader;

CREATE TABLE IF NOT EXISTS reader.saved_books (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- user_id es de user-service (otra base): UUID plano, sin FK.
    user_id    UUID        NOT NULL,
    -- book_id si lleva FK real aunque cruce de esquema: content vive en esta
    -- misma base, y Postgres permite referenciar entre esquemas.
    book_id    UUID        NOT NULL REFERENCES content.books(id) ON DELETE CASCADE,
    kind       VARCHAR(20) NOT NULL CHECK (kind IN ('favorite', 'read_later')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Un lector no guarda el mismo libro dos veces en la misma categoria, pero
    -- favorite y read_later si pueden coexistir (son filas distintas).
    CONSTRAINT saved_books_user_id_book_id_kind_key UNIQUE (user_id, book_id, kind)
);

-- "Mi biblioteca" siempre consulta por user_id, con filtro opcional por kind.
CREATE INDEX IF NOT EXISTS saved_books_user_id_kind_idx ON reader.saved_books (user_id, kind);
