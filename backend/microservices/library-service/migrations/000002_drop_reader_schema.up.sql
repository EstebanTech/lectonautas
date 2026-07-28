-- El esquema reader (la biblioteca del lector: favoritos y leer mas tarde) se
-- va de este servicio. Guardar un libro es algo que hace el lector, no el
-- autor, y vive en el servicio que lo modele; aqui solo queda la obra.
--
-- Este numero lo ocupaba la migracion que creaba el esquema. Se reemplazo en
-- vez de encadenar un create+drop porque no hay tabla de migraciones aplicadas:
-- el compose corre todos los *.up.sql en orden en cada arranque, asi que lo que
-- vale es el estado final. El DROP se queda para limpiar las bases locales que
-- ya tenian el esquema; en una base nueva no encuentra nada y no hace nada.
DROP SCHEMA IF EXISTS reader CASCADE;
