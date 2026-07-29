<div align="center">

```
██╗     ███████╗ ██████╗████████╗ ██████╗ ███╗   ██╗ █████╗ ██╗   ██╗████████╗ █████╗ ███████╗
██║     ██╔════╝██╔════╝╚══██╔══╝██╔═══██╗████╗  ██║██╔══██╗██║   ██║╚══██╔══╝██╔══██╗██╔════╝
██║     █████╗  ██║        ██║   ██║   ██║██╔██╗ ██║███████║██║   ██║   ██║   ███████║███████╗
██║     ██╔══╝  ██║        ██║   ██║   ██║██║╚██╗██║██╔══██║██║   ██║   ██║   ██╔══██║╚════██║
███████╗███████╗╚██████╗   ██║   ╚██████╔╝██║ ╚████║██║  ██║╚██████╔╝   ██║   ██║  ██║███████║
╚══════╝╚══════╝ ╚═════╝   ╚═╝    ╚═════╝ ╚═╝  ╚═══╝╚═╝  ╚═╝ ╚═════╝    ╚═╝   ╚═╝  ╚═╝╚══════╝
```

**Las mejores historias no solo se leen, también se viven.**

</div>

---

Un **lectonauta** no abre un libro: se embarca en él.

Lectonautas es una red social literaria que conecta lectores y escritores, donde
las historias pueden ser **interactivas**: cada decisión del lector desvía la
ruta y el relato termina en un puerto distinto. La misma historia, leída dos
veces, no es la misma historia.

El proyecto busca usar tecnología para crear nuevas formas de contar historias y
conectar personas a través de la literatura.

## Bitácora técnica

Monorepo de Lectonautas. El backend son microservicios en Go que se comunican
entre sí por **gRPC**, detrás de un **API Gateway** con Envoy que expone HTTP/JSON
al cliente y lo traduce a gRPC.

## Tecnologías

- **Go 1.24** — microservicios
- **gRPC** + **Protocol Buffers** — comunicación entre servicios
- **Envoy** — API Gateway: transcoding HTTP↔gRPC, CORS, rate limiting y WebSocket
- **PostgreSQL 16** — persistencia
- **Valkey** — alternativa a Redis para rate limiting, cache y el pub/sub que reparte los eventos en tiempo real
- **WebSocket** — interacciones en vivo (`interaction-service`)
- **Docker Compose** — entorno local

## Estructura

```
lectonautas/
├── backend/
│   ├── microservices/
│   │   ├── user-service/          # Usuarios, login y sesiones
│   │   ├── library-service/       # Libros, capítulos, sagas y géneros
│   │   └── interaction-service/   # Me gusta, comentarios y calificaciones (+ WebSocket)
│   └── shared/                    # Infraestructura común: sin dominio dentro
├── gateway/                       # Config de Envoy y rate limiting
├── scripts/                       # Prueba de humo y chequeo del descriptor
└── docker-compose.yml
```

Cada servicio es un módulo Go independiente y `backend/shared` es un cuarto
módulo que los tres usan con un `replace` relativo. Ahí vive **solo lo que no
sabe nada del dominio**: el `.env` global, el pool de Postgres, el cliente de
Valkey con su cache versionado, los interceptores gRPC y el logging. Si algo de
ahí necesitara saber qué es un libro, no es de ahí.

## Requisitos

- **Docker Desktop**, o Docker Engine con Compose. Nada más: Go, PostgreSQL, Valkey
  y Envoy corren dentro de contenedores; no hace falta instalarlos en la máquina.

## Levantar el entorno

**Windows / PowerShell**

```powershell
Copy-Item .env.example .env
docker compose up -d --build
```

**Linux / macOS**

```bash
cp .env.example .env
docker compose up -d --build
```

El primer comando solo hace falta la primera vez. `.env.example` trae valores por
defecto que funcionan en local, y las migraciones de la base de datos **se aplican
solas** al arrancar, así que no hay pasos manuales. Cuando los contenedores estén
arriba, la API queda en `http://localhost:8080`.

Los tres servicios exponen el **health check de gRPC** y el compose los encadena
con `service_healthy`: el gateway no arranca hasta que los tres responden de
verdad, no solo hasta que sus contenedores existen. Con `--wait`, el comando de
arriba no vuelve hasta que todo está listo:

```bash
docker compose up -d --build --wait
```

Para comprobar que quedó bien, hay una prueba de humo que recorre el camino
completo —crear cuenta, publicar un libro, interactuar con él— cruzando los tres
servicios:

```bash
bash scripts/smoke-test.sh
```

Servicios y puertos:

- Gateway HTTP: `:8080`
- Admin de Envoy: `:9901`
- PostgreSQL: `:5433`
- Valkey: `:6379`

## API HTTP — user-service

| Método | Ruta | Acción |
|---|---|---|
| `POST` | `/v1/users` | Crear usuario |
| `GET` | `/v1/users/{id}` | Obtener usuario |
| `PATCH` | `/v1/users/{id}` | Actualizar usuario |
| `DELETE` | `/v1/users/{id}` | Eliminar usuario |
| `GET` | `/v1/users` | Obtener todos los usuarios |
| `POST` | `/v1/auth/login` | Login: devuelve un token de sesión |
| `GET` | `/v1/auth/me` | Usuario del token enviado en el header `Authorization: Bearer` |
| `POST` | `/v1/auth/logout` | Cierra la sesión del token |

### Autenticación

`login` genera un token aleatorio, lo devuelve **en crudo** al cliente una sola
vez y guarda solo su **hash** SHA-256 en la tabla `session` de la BD y en Valkey
con TTL de 2h. En cada request autenticado el token del header se hashea y se
busca primero en Valkey; si no está, se valida contra la BD y se repuebla Valkey
con el tiempo restante, siguiendo un patrón cache-aside.

`user-service` es el único dueño de las sesiones. Los demás servicios no tienen
esa tabla: resuelven el token llamándolo por gRPC (`ValidateSession`), que no
está expuesta como endpoint HTTP y además queda bloqueada en el gateway.

## API HTTP — library-service

Los libros de otros solo se ven **publicados**. Un borrador ajeno responde `404`,
no `403`: que exista tampoco es público.

| Método | Ruta | Acción |
|---|---|---|
| `POST` | `/v1/books` | Crear libro (vacío; admite hasta 4 `genres`) |
| `GET` | `/v1/books` | Listado público y paginado: **solo publicados** (filtro `?genre=`) |
| `GET` | `/v1/books/mine` | Los libros del autor del token, en **cualquier** estado y **sin paginar** |
| `GET` | `/v1/books/{id}` | Libro con sus capítulos |
| `PATCH` | `/v1/books/{id}` | Actualizar libro |
| `DELETE` | `/v1/books/{id}` | Eliminar libro (arrastra capítulos por CASCADE) |
| `POST` | `/v1/books/{bookId}/chapters` | Crear capítulo |
| `GET` | `/v1/books/{bookId}/chapters/{id}` | Obtener capítulo |
| `PATCH` | `/v1/books/{bookId}/chapters/{id}` | Actualizar capítulo (publicar exige libro publicado) |
| `DELETE` | `/v1/books/{bookId}/chapters/{id}` | Eliminar capítulo (nunca el último) |
| `PATCH` | `/v1/books/{bookId}/chapters/reorder` | Reordenar: manda todos los ids en orden |
| `POST` | `/v1/sagas` | Crear saga |
| `GET` | `/v1/sagas` | Listado público de sagas |
| `GET` | `/v1/sagas/mine` | Las sagas del autor del token |
| `GET` | `/v1/sagas/{id}` | Saga con sus libros ordenados |
| `PATCH` | `/v1/sagas/{id}` | Actualizar saga |
| `DELETE` | `/v1/sagas/{id}` | Eliminar saga (los libros no se tocan) |
| `POST` | `/v1/sagas/{sagaId}/books` | Vincular un libro a la saga |
| `DELETE` | `/v1/sagas/{sagaId}/books/{bookId}` | Desvincular |
| `PATCH` | `/v1/sagas/{sagaId}/books/reorder` | Reordenar los libros de la saga |
| `GET` | `/v1/genres` | Catálogo de géneros (lista fija, pública) |
| `PUT` | `/v1/books/{bookId}/genres` | Fijar los géneros del libro (reemplaza la lista) |

Los endpoints `/mine` **no aceptan `author_id`**: sale del token. Es lo que
impide pedir la obra inédita de otro.

`GET /v1/books/mine` es el único listado **sin paginar**: devuelve la obra
completa del autor y su respuesta no lleva `page` ni `pageSize`. Se lo permite
porque lo que devuelve está acotado por naturaleza —los libros que una persona
escribió—, mientras que el listado público, que crece sin límite, sigue paginado
a 20 por página (máximo 100).

### Estados de libro y capítulo

Un capítulo **no se puede publicar mientras el libro no esté publicado**
(`400`, `FailedPrecondition`, igual que publicar un libro vacío). Publicar es
"esto ya se puede leer", y en un libro
que no está a la vista de nadie eso no significa nada. El orden de trabajo que
impone:

```
crear libro (draft) → escribir capítulos (draft) → publicar libro → publicar capítulos
```

No se traba, porque **publicar el libro solo exige que tenga capítulos**, no que
estén publicados. Despublicar el libro no degrada sus capítulos: se quedan como
estaban, para no perder el estado de trabajo del autor.

Libros y capítulos llevan `publishedAt`: **cuándo se publicó por primera vez**,
ausente si nunca. Es lo que distingue un borrador recién escrito de uno que
estuvo publicado y se retiró, y lo que sirve para ordenar novedades. No se borra
al despublicar ni se pisa al volver a publicar — si se pisara, bastaría con
despublicar y republicar para colarse otra vez en las novedades. La fecha se fija
en el mismo `UPDATE` con un `COALESCE`, así que no hay ventana entre leerla y
escribirla.

`chapterCount` cuenta **solo los capítulos publicados**, y es el mismo número
para todos —también para el autor—: un borrador todavía no es parte del libro.
Al autor, `GET /v1/books/{id}` sí le devuelve sus borradores en la lista de
capítulos; son dos preguntas distintas, cuánto hay publicado y qué tiene escrito.
Un lector ajeno solo ve los publicados.

Un libro lleva **hasta 4 géneros**, elegidos del catálogo de `/v1/genres`. No se
tocan con `PATCH /v1/books/{id}`: van por `PUT .../genres`, que reemplaza la
lista entera (una lista vacía deja el libro sin géneros). Está aparte porque en
proto3 un campo `repeated` no distingue "no lo mandes" de "déjalo vacío", así
que dentro del PATCH no habría forma de quitarlos todos.

Toda lectura se cachea en Valkey con TTL de 15 minutos. La invalidación es por
versión: cada clave lleva dentro un contador que toda escritura incrementa, así
que las claves viejas quedan inalcanzables de golpe.

## API HTTP — interaction-service

Lo que el lector hace con la obra ajena. Solo se puede interactuar con libros
**publicados**: un borrador o un libro inexistente responde `404`, porque este
servicio se lo pregunta a `library-service` sin token y ésa es la regla de allá.

| Método | Ruta | Acción |
|---|---|---|
| `POST` | `/v1/books/{bookId}/likes` | Me gusta (idempotente) |
| `DELETE` | `/v1/books/{bookId}/likes` | Quitarlo (idempotente) |
| `GET` | `/v1/books/{bookId}/likes` | Conteo; con token, además `likedByMe` |
| `POST` | `/v1/books/{bookId}/comments` | Comentar |
| `GET` | `/v1/books/{bookId}/comments` | Listado público y paginado |
| `PATCH` | `/v1/books/{bookId}/comments/{id}` | Editar (**solo su autor**) |
| `DELETE` | `/v1/books/{bookId}/comments/{id}` | Borrar (su autor **o** el autor del libro) |
| `PUT` | `/v1/books/{bookId}/rating` | Calificar de 1 a 5 (reemplaza la tuya) |
| `GET` | `/v1/books/{bookId}/rating` | Promedio y votos; con token, `myScore` |
| `DELETE` | `/v1/books/{bookId}/rating` | Retirar tu calificación |
| `GET` | `/v1/books/{bookId}/interactions` | Los tres resúmenes en una llamada |

Dar me gusta y quitarlo son **idempotentes**: repetirlos deja el mismo estado y
devuelven el conteo, sin error. El cliente típico es un botón, y un doble clic o
un reintento tras un timeout no son un caso de error. La calificación es `PUT`
por lo mismo: un lector tiene **una** nota por libro, y volver a calificar
reemplaza la suya en vez de sumar otro voto.

Los comentarios traen `userId`, no el nombre: este servicio no es dueño de los
perfiles, igual que `library-service` devuelve `authorId` y no el nombre del autor.

### Tiempo real — WebSocket

```
GET ws://localhost:8080/v1/ws/books/{bookId}[?token=<token>]
```

Canal **de ida**: el servidor manda, el cliente escucha. Las escrituras siguen
yendo por REST, para no duplicar sobre otro protocolo la validación y los
permisos que ya viven ahí. Nada más conectar llega un `snapshot` con los números
de ahora, y después solo los cambios:

| `type` | Trae |
|---|---|
| `snapshot` | `likes`, `rating`, `commentCount` al conectar |
| `like_changed` | `likes.count` |
| `rating_changed` | `rating.average`, `rating.count` |
| `comment_created` / `comment_updated` | `comment` completo + `commentCount` |
| `comment_deleted` | `commentId` + `commentCount` |

El token va en la query y es **opcional**: la API de WebSocket del navegador no
deja poner cabeceras en el handshake, y el stream es público. Si se manda uno
inválido, la conexión se rechaza con `401` en vez de degradarla a anónima en
silencio. Un libro que no existe o no está publicado responde `404` **antes** del
upgrade, para que el error se vea como un error HTTP normal.

Los eventos no se reparten en memoria sino por **pub/sub de Valkey**: quien
escribe y quien escucha no tienen por qué estar en la misma réplica. Es
best-effort a propósito — si Valkey se cae se pierden avisos, no datos, y un
`GET` devuelve siempre el estado correcto.

### Limpieza entre servicios

Nada de esto tiene foreign keys: los libros y los usuarios viven en otras bases.
Cuando algo desaparece, su servicio dueño lo avisa por gRPC:

- **Se borra un libro** → `library-service` pide borrar sus interacciones.
- **Se da de baja una cuenta** → `user-service` pide borrar lo que dejó como
  lector, y `library-service` limpia además las interacciones de los libros que
  se lleva por delante.

Las dos RPC de limpieza no tienen ruta HTTP y quedan bloqueadas en el gateway,
igual que `ValidateSession`. Pero el gateway solo cubre a quien entra por la
puerta: dentro de la red cualquiera podría llamarlas, y su única credencial
sería un id que además es público. Por eso además exigen un **secreto compartido**
(`INTERNAL_SERVICE_TOKEN`) que viaja como metadata gRPC en las llamadas entre
servicios. Sin él responden `NotFound`, la misma respuesta que da el gateway:
desde fuera no hay forma de saber que existen.

No tiene valor por defecto en el código a propósito. Si falta, los servicios no
arrancan —es preferible a arrancar pareciendo protegidos sin estarlo.

## Cómo se observa

Los tres servicios escriben **log estructurado en JSON** (nivel por `LOG_LEVEL`)
y cada línea lleva el `request_id` de la petición que la produjo. El id lo genera
Envoy, viaja a los vecinos en cada llamada gRPC y vuelve al cliente en la
cabecera `x-request-id`, así que una petición se sigue entera aunque cruce los
tres servicios:

```json
{"level":"INFO","msg":"request ok","service":"interaction-service","request_id":"1ed2bd33…","method":"/interaction.v1.InteractionService/CreateComment"}
{"level":"INFO","msg":"request ok","service":"library-service","request_id":"1ed2bd33…","method":"/library.v1.LibraryService/GetBook"}
{"level":"INFO","msg":"request ok","service":"user-service","request_id":"1ed2bd33…","method":"/user.v1.UserService/ValidateSession"}
```

Un comentario, tres servicios, un solo hilo del que tirar.

## Migraciones

Cada base tiene una tabla `public.schema_migrations` con lo que ya se aplicó, y
el migrador del compose corre **solo lo que falta**. Antes reejecutaba todos los
`*.up.sql` en cada arranque y la idempotencia se sostenía a mano (`IF NOT EXISTS`,
`ON CONFLICT`, `WHERE ... IS NULL`); funcionaba, pero convertía cualquier
migración no reejecutable —un `DROP COLUMN`, un backfill que no se puede
repetir— en una rotura silenciosa.

Cada archivo va en una sola transacción junto con su registro: si falla a la
mitad, no queda ni el cambio a medias ni la marca de aplicada.

## Verificación

`make` no es imprescindible; entre paréntesis va el comando directo.

| Qué | Comando |
|---|---|
| Compilar, vet y tests de los cuatro módulos | `make check` |
| Tests de un módulo | `cd backend/microservices/<servicio> && go test ./... -count=1` |
| Tests contra Postgres real | `make -C backend/microservices/<servicio> test-integration` |
| Descriptor del gateway al día | `bash scripts/check-descriptor.sh` |
| Prueba de humo end-to-end | `bash scripts/smoke-test.sh` |

El CI (`.github/workflows/ci.yml`) corre los cuatro módulos en paralelo con
`gofmt`, `go vet` y los tests bajo `-race`, comprueba el descriptor y además
levanta el entorno completo con `docker compose --wait` para pasarle la prueba
de humo. Eso último es lo que atrapa lo que ningún test unitario ve: que las
imágenes compilan, que las migraciones corren y que el encadenado del compose
termina.

## Estado

- `user-service`: CRUD de usuarios más login y sesiones con tabla `session` y
  Valkey. Validación, CRUD, cache-aside e invalidación con dobles en memoria,
  más las reglas de vigencia de la sesión contra Postgres real
  (`test-integration`).
- `library-service`: libros, capítulos, sagas y géneros. Las reglas de
  visibilidad y propiedad con dobles en memoria, más las de repositorio contra
  Postgres real —transacciones, CASCADE, los UNIQUE del esquema y el tope de
  géneros por libro—. Estas últimas se saltan solas si no hay base configurada.
- `interaction-service`: me gusta, comentarios y calificaciones, con avisos en
  vivo por WebSocket. Las reglas de idempotencia, permisos y validación con
  dobles en memoria, el hub de suscripciones bajo `-race`, y contra Postgres
  real el upsert, el `ON CONFLICT`, el CHECK del rango 1..5 y las transacciones
  de limpieza.
- `backend/shared`: el control de acceso de las llamadas entre servicios y la
  propagación del id de petición, que es donde un fallo abierto no se notaría
  hasta que alguien lo usara.
- Gateway probado end-to-end: transcoding, CORS, upgrade a WebSocket y rate
  limiting por clase de ruta —10/min el login, 200/min las lecturas, 300/min el
  catálogo de géneros, 80/min todo lo demás—, todo por IP.
- Fuera de alcance por ahora: **leer más tarde**, la otra mitad de la biblioteca
  del lector que salió de `library-service`. No es una interacción con la obra
  ajena sino una lista privada de lectura, así que no encaja en
  `interaction-service` tal como quedó; le toca servicio propio o volver como
  recurso aparte.
