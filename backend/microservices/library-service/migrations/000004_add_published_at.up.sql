-- Fecha de publicacion de libros y capitulos.
--
-- El estado (status) dice DONDE esta la obra ahora; esto dice CUANDO llego. Son
-- dos preguntas distintas y por eso son dos columnas, no una tabla aparte: el
-- dato es 1:1 con la fila, se lee siempre junto a ella y no tiene por que
-- costar un JOIN.
--
-- NULL significa "nunca se ha publicado", que es lo que distingue un borrador
-- recien escrito de uno que estuvo publicado y se retiro.
ALTER TABLE content.books ADD COLUMN IF NOT EXISTS published_at TIMESTAMPTZ;
ALTER TABLE content.chapters ADD COLUMN IF NOT EXISTS published_at TIMESTAMPTZ;

-- Relleno para lo que ya estaba publicado antes de existir esta columna. No hay
-- forma de saber la fecha real —nadie la guardaba—, asi que se usa updated_at
-- como aproximacion: es la ultima vez que la fila cambio, y para una obra ya
-- publicada suele ser esa misma publicacion o algo posterior.
--
-- El WHERE lo hace repetible: el compose corre esta migracion en cada arranque
-- y una fila que ya tenga fecha no se vuelve a tocar.
UPDATE content.books
   SET published_at = updated_at
 WHERE status = 'published' AND published_at IS NULL;

UPDATE content.chapters
   SET published_at = updated_at
 WHERE status = 'published' AND published_at IS NULL;

-- Los listados de novedades ordenan por fecha de publicacion entre lo
-- publicado, que es el mismo patron que ya sirve books_status_created_at_idx
-- para el orden por creacion.
CREATE INDEX IF NOT EXISTS books_published_at_idx
    ON content.books (published_at DESC)
    WHERE status = 'published';
