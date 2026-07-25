# library-service — Base de datos y lógica de negocio

Documento de referencia para implementar `library-service` en Go.
Describe primero la estructura de la base de datos y luego las reglas de
negocio que se derivan de ella.

---

## 1. Contexto del servicio

- **Lenguaje:** Go.
- **Base de datos:** PostgreSQL, una sola base de datos (`lectonautas_library`) en su propio contenedor.
- **Rol:** microservicio dentro de un sistema mayor (Lectonautas). Se comunica con otros servicios vía gRPC y queda detrás de un gateway (Envoy).
- **Organización interna:** dos módulos (paquetes de Go) con fronteras claras:
  - `content` → contenido del autor: libros, capítulos, sagas.
  - `reader` → biblioteca del lector: favoritos / guardar para leer más tarde.
- Estos dos módulos comparten la misma base de datos, cada uno en su propio **esquema** de Postgres (`content` y `reader`).

### Regla transversal: referencias entre servicios

Los usuarios viven en otro servicio (`user-service`), en otra base de datos.
Por eso las columnas que apuntan a un usuario (`author_id`, `user_id`) se
guardan como **UUID plano, SIN foreign key**. Su validez se comprueba a nivel
de aplicación (o vía gRPC contra `user-service`), nunca con una FK.

En cambio, las referencias **dentro de esta misma base** sí usan foreign keys
reales (ej. `chapters.book_id → books.id`), incluso cuando cruzan de un esquema
a otro (`reader.saved_books.book_id → content.books.id`, permitido por Postgres).

---

## 2. Estructura de la base de datos

### Esquema `content`

#### Tabla `content.books`
Cada libro, publicado o en borrador.

| Columna | Tipo | Restricciones | Descripción |
|---|---|---|---|
| id | UUID | PK | Identificador interno del libro. |
| author_id | UUID | NOT NULL | user_id del autor (user-service). UUID plano, sin FK. |
| title | VARCHAR | NOT NULL | Título del libro. |
| description | TEXT | | Sinopsis. |
| cover_url | VARCHAR | | URL de la portada. |
| status | ENUM/VARCHAR | NOT NULL, DEFAULT 'draft' | draft / published / archived. |
| created_at | TIMESTAMP | NOT NULL, DEFAULT now() | |
| updated_at | TIMESTAMP | NOT NULL, DEFAULT now() | |

#### Tabla `content.chapters`
Cuerpo de los libros. **Todo el contenido vive aquí**, siempre.

| Columna | Tipo | Restricciones | Descripción |
|---|---|---|---|
| id | UUID | PK | |
| book_id | UUID | NOT NULL, FK → content.books(id) ON DELETE CASCADE | Libro al que pertenece. |
| title | VARCHAR | NOT NULL | Título del capítulo. |
| content | TEXT | | Texto del capítulo. |
| position | INT | NOT NULL | Orden dentro del libro (1, 2, 3...). |
| status | ENUM/VARCHAR | NOT NULL, DEFAULT 'draft' | draft / published. |
| created_at | TIMESTAMP | NOT NULL, DEFAULT now() | |
| updated_at | TIMESTAMP | NOT NULL, DEFAULT now() | |

Restricción única: `(book_id, position)` — no puede haber dos capítulos en la
misma posición dentro de un libro.

#### Tabla `content.sagas`
Colecciones ordenadas de libros (trilogías, series).

| Columna | Tipo | Restricciones | Descripción |
|---|---|---|---|
| id | UUID | PK | |
| author_id | UUID | NOT NULL | UUID plano, sin FK. |
| title | VARCHAR | NOT NULL | |
| description | TEXT | | |
| created_at | TIMESTAMP | NOT NULL, DEFAULT now() | |
| updated_at | TIMESTAMP | NOT NULL, DEFAULT now() | |

#### Tabla `content.saga_books`
Tabla puente saga↔libro. Aquí vive la relación; no se expone como recurso propio.

| Columna | Tipo | Restricciones | Descripción |
|---|---|---|---|
| saga_id | UUID | NOT NULL, FK → content.sagas(id) ON DELETE CASCADE | |
| book_id | UUID | NOT NULL, FK → content.books(id) ON DELETE CASCADE | |
| position | INT | NOT NULL | Orden del libro dentro de la saga. |

Primary key compuesta: `(saga_id, book_id)` — un libro no se repite en la misma saga.
Sin fila aquí = el libro no pertenece a ninguna saga.

### Esquema `reader`

#### Tabla `reader.saved_books`
Biblioteca del lector: libros guardados como favoritos o para leer más tarde.

| Columna | Tipo | Restricciones | Descripción |
|---|---|---|---|
| id | UUID | PK | |
| user_id | UUID | NOT NULL | El lector (user-service). UUID plano, sin FK. |
| book_id | UUID | NOT NULL, FK → content.books(id) ON DELETE CASCADE | FK que cruza de reader a content. |
| kind | ENUM/VARCHAR | NOT NULL | favorite / read_later. |
| created_at | TIMESTAMP | NOT NULL, DEFAULT now() | |

Restricción única: `(user_id, book_id, kind)` — un lector no guarda el mismo libro
dos veces en la misma categoría. `favorite` y `read_later` pueden coexistir para
el mismo libro (son filas distintas).

### Relaciones

```
content.books  1──N  content.chapters        (chapters.book_id)
content.sagas  1──N  content.saga_books       (saga_books.saga_id)
content.books  1──N  content.saga_books       (saga_books.book_id)
content.books  1──N  reader.saved_books       (saved_books.book_id, cross-schema FK)
```

---

## 3. Lógica de negocio (Go)

### Estructura de paquetes sugerida
```
internal/
  content/   → handlers, servicios y acceso a datos de books, chapters, sagas
  reader/    → handlers, servicios y acceso a datos de saved_books
```
Un módulo no accede directamente a las tablas del otro: si `reader` necesita
datos de un libro, llama a la API interna del paquete `content`.

### Reglas del módulo `content`

1. **Crear un libro crea su primer capítulo.**
   `POST /books` inserta el libro **y** su capítulo con `position = 1`, en una
   sola transacción. Nunca debe quedar un libro sin al menos un capítulo. Si
   falla la inserción del capítulo, se revierte todo.

2. **El contenido siempre está en `chapters`.**
   `books` no tiene columna de texto. Un libro de un solo capítulo simplemente
   tiene una fila en `chapters`. La presentación (mostrar "Capítulo 1" o solo el
   título) es responsabilidad del cliente, no del backend.

3. **No se puede borrar el último capítulo de un libro.**
   Un libro siempre debe conservar al menos un capítulo. Borrar el libro completo
   es otra operación (elimina el libro y, por CASCADE, sus capítulos).

4. **Reordenar capítulos** actualiza el campo `position`.

5. **Estados y visibilidad.**
   - `books.status`: draft / published / archived.
   - `chapters.status`: draft / published.
   - Un lector nunca ve borradores ajenos. Los listados públicos filtran por
     `status = 'published'`.

6. **Sagas.**
   - Vincular un libro a una saga = insertar en `saga_books` (no es un CRUD propio,
     es una acción sobre la saga).
   - Un libro puede vincularse a una saga aunque el libro se haya creado antes.
   - Un libro puede no pertenecer a ninguna saga.
   - Reordenar libros de una saga = actualizar `saga_books.position`.

### Reglas del módulo `reader`

1. Guardar un libro = insertar en `saved_books` con el `kind` correspondiente.
2. `favorite` y `read_later` son independientes: el mismo libro puede estar en ambas.
3. Listar "mi biblioteca" = consultar `saved_books` del `user_id`, con JOIN a
   `content.books` para traer título, portada, etc. (posible gracias a la FK
   cross-schema).
4. Quitar un libro de la biblioteca = borrar la fila correspondiente.

### Autorización (aplica a todo el servicio)

- El servicio recibe un token de sesión pero **no tiene la tabla de sesiones**
  (vive en `user-service`). La validación del token se resuelve consultando
  Valkey (donde `user-service` cachea las sesiones) o vía gRPC a `user-service`.
  (Decisión pendiente de cerrar en el proyecto.)
- **Verificación de propiedad:** en toda operación de escritura sobre libros,
  capítulos o sagas, solo el `author_id` dueño puede editar o borrar. Un usuario
  autenticado no puede modificar contenido de otro.

---

## 4. Endpoints

### Libros (recurso propio)
```
POST   /books
GET    /books                 (listado público, paginado, filtros; solo published)
GET    /books/mine            (los libros del autor autenticado, en cualquier estado)
GET    /books/:id             (libro + capítulos)
PATCH  /books/:id
DELETE /books/:id
```

`GET /books` y `GET /books/mine` están separados a propósito: un endpoint, una
regla de visibilidad. El público filtra siempre por `status = 'published'`, haya
token o no, y rechaza que le pidan otro estado. El del autor exige token y **no
recibe `author_id`**: sale de la sesión, no del cliente, que es lo único que
impide pedir la obra inédita de otro.

### Capítulos (anidados bajo el libro)
```
POST   /books/:bookId/chapters
GET    /books/:bookId/chapters/:id
PATCH  /books/:bookId/chapters/:id
DELETE /books/:bookId/chapters/:id
PATCH  /books/:bookId/chapters/reorder
```

### Sagas (recurso propio)
```
POST   /sagas
GET    /sagas                 (listado público, paginado)
GET    /sagas/mine            (las sagas del autor autenticado)
GET    /sagas/:id             (saga + libros ordenados)
PATCH  /sagas/:id
DELETE /sagas/:id
POST   /sagas/:id/books       (vincular libro)
DELETE /sagas/:id/books/:bookId   (desvincular)
PATCH  /sagas/:id/books/reorder
```

### Biblioteca del lector (recurso propio)
```
POST   /library              (guardar libro: body con book_id + kind)
GET    /library              (mis libros guardados; filtro opcional por kind)
DELETE /library/:bookId      (quitar; kind opcional)
```

Nota: aunque el servicio tiene dos módulos internos, todos los endpoints se
exponen por igual detrás del gateway. El cliente no distingue módulos.

---

## 5. Fuera de alcance

Likes, comentarios y calificaciones (ratings) **NO** pertenecen a este servicio.
Viven en un `interaction-service` separado, con su propia base de datos, y
referencian `book_id` / `chapter_id` como UUID plano (sin FK, otra base).
