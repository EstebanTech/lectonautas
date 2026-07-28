-- Esquema interaction: lo que el lector hace con la obra ajena.
CREATE SCHEMA IF NOT EXISTS interaction;

-- book_id y user_id son de otros servicios, cada uno con su propia base: por
-- eso son UUID planos y SIN foreign key, igual que el author_id de un libro.
-- Que el libro exista y este publicado lo comprueba la aplicacion (gRPC contra
-- library-service) antes de escribir, y la limpieza al borrar un libro o una
-- cuenta llega por RPC, no por CASCADE.

-- Me gusta. La PK compuesta es toda la regla: un lector da me gusta una vez a
-- un libro, y volver a darlo no crea otra fila.
CREATE TABLE IF NOT EXISTS interaction.likes (
    book_id    UUID        NOT NULL,
    user_id    UUID        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (book_id, user_id)
);

-- Para la baja de cuenta, que borra por usuario; la PK ya cubre el otro lado.
CREATE INDEX IF NOT EXISTS likes_user_id_idx ON interaction.likes (user_id);

CREATE TABLE IF NOT EXISTS interaction.comments (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    book_id    UUID        NOT NULL,
    user_id    UUID        NOT NULL,
    body       TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- El listado de un libro sale siempre ordenado por fecha descendente.
CREATE INDEX IF NOT EXISTS comments_book_id_created_at_idx
    ON interaction.comments (book_id, created_at DESC);
CREATE INDEX IF NOT EXISTS comments_user_id_idx ON interaction.comments (user_id);

-- Calificaciones. Como en likes, la PK compuesta dice que un lector tiene una
-- sola calificacion por libro: volver a calificar es un UPDATE, no otra fila.
CREATE TABLE IF NOT EXISTS interaction.ratings (
    book_id    UUID        NOT NULL,
    user_id    UUID        NOT NULL,
    score      SMALLINT    NOT NULL CHECK (score BETWEEN 1 AND 5),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (book_id, user_id)
);

CREATE INDEX IF NOT EXISTS ratings_user_id_idx ON interaction.ratings (user_id);
