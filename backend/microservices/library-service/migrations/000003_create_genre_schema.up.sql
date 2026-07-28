-- Generos de los libros. Un libro puede tener varios, hasta un maximo de 4.

-- Catalogo cerrado: los generos no se crean por API, se agregan aqui. Por eso
-- la clave es el slug y no un id sintetico — es un identificador estable, corto
-- y legible, y asi content.book_genres ya dice a que genero apunta sin JOIN.
CREATE TABLE IF NOT EXISTS content.genres (
    slug VARCHAR(50) PRIMARY KEY,
    -- La etiqueta que ve el lector. En espanol, a diferencia del slug: uno es
    -- dato de la API y el otro texto de interfaz.
    name VARCHAR(80) NOT NULL UNIQUE
);

-- El ON CONFLICT hace la insercion repetible: el compose corre esta migracion
-- en cada arranque, y asi ademas una correccion de nombre se propaga sola.
INSERT INTO content.genres (slug, name) VALUES
    ('fantasy',         'Fantasía'),
    ('science-fiction', 'Ciencia ficción'),
    ('mystery',         'Misterio'),
    ('thriller',        'Suspenso'),
    ('horror',          'Terror'),
    ('romance',         'Romance'),
    ('adventure',       'Aventura'),
    ('historical',      'Histórica'),
    ('drama',           'Drama'),
    ('comedy',          'Comedia'),
    ('poetry',          'Poesía'),
    ('young-adult',     'Juvenil'),
    ('children',        'Infantil'),
    ('non-fiction',     'No ficción'),
    ('biography',       'Biografía'),
    ('self-help',       'Autoayuda'),
    ('essay',           'Ensayo'),
    ('fanfiction',      'Fanfiction')
ON CONFLICT (slug) DO UPDATE SET name = EXCLUDED.name;

-- Tabla puente libro<->genero. La PK compuesta ya impide repetir un genero en
-- el mismo libro. El genero no se borra en cascada a proposito (RESTRICT):
-- sacar un genero del catalogo teniendo libros que lo usan tiene que fallar y
-- obligar a decidir que pasa con esos libros.
CREATE TABLE IF NOT EXISTS content.book_genres (
    book_id UUID        NOT NULL REFERENCES content.books(id) ON DELETE CASCADE,
    genre   VARCHAR(50) NOT NULL REFERENCES content.genres(slug) ON DELETE RESTRICT,
    PRIMARY KEY (book_id, genre)
);

-- El listado publico filtra por genero; la PK solo sirve para el otro sentido.
CREATE INDEX IF NOT EXISTS book_genres_genre_idx ON content.book_genres (genre);

-- El tope de 4 generos por libro no se puede expresar con un CHECK, que solo ve
-- la fila que se inserta. El servicio ya lo valida antes de escribir; esto es la
-- red de abajo, para que una escritura directa a la BD tampoco lo pueda romper.
CREATE OR REPLACE FUNCTION content.enforce_book_genre_limit() RETURNS trigger AS $$
BEGIN
    IF (SELECT count(*) FROM content.book_genres WHERE book_id = NEW.book_id) > 4 THEN
        -- CONSTRAINT viaja en el error igual que si lo hubiera lanzado un CHECK
        -- de verdad, y es lo que mira el repositorio para distinguirlo de
        -- cualquier otro check_violation del esquema.
        RAISE EXCEPTION 'a book cannot have more than 4 genres'
            USING ERRCODE = 'check_violation', CONSTRAINT = 'book_genres_max_per_book';
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

-- AFTER y no BEFORE: cuenta con la fila nueva ya puesta, asi que el limite se
-- lee tal cual (> 4) sin sumar uno a mano.
DROP TRIGGER IF EXISTS book_genres_max_per_book ON content.book_genres;
CREATE TRIGGER book_genres_max_per_book
    AFTER INSERT ON content.book_genres
    FOR EACH ROW EXECUTE FUNCTION content.enforce_book_genre_limit();
