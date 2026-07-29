#!/usr/bin/env bash
# Prueba de humo contra el entorno levantado: recorre el camino real de una
# petición de punta a punta, cruzando los tres servicios y el gateway.
#
# No sustituye a los tests de Go, que prueban las reglas. Esto prueba lo otro:
# que el transcoder traduce, que el token vale en los tres servicios, que el
# secreto interno deja pasar las llamadas entre vecinos y que el rate limiting
# distingue una ruta de otra. Nada de eso se puede afirmar sin el sistema
# entero arriba.
#
#   bash scripts/smoke-test.sh [http://localhost:8080]
set -euo pipefail

BASE="${1:-http://localhost:8080}"
SUF="${RANDOM}${RANDOM}"
EMAIL="smoke$SUF@lectonautas.dev"
PASS="password123"

ok()   { echo "  ok: $1"; }
fatal() { echo "FALLO: $1" >&2; exit 1; }

# jsonval saca un valor escalar de la respuesta. El gateway responde el JSON
# con espacios (add_whitespace), asi que el patron los tiene en cuenta.
jsonval() { grep -o "\"$1\": *\"[^\"]*\"" | head -1 | sed 's/.*: *"//; s/"$//'; }

echo "== usuarios y sesion =="
curl -sf -X POST "$BASE/v1/users" -H 'Content-Type: application/json' \
  -d "{\"email\":\"$EMAIL\",\"username\":\"smoke$SUF\",\"password\":\"$PASS\"}" >/dev/null \
  || fatal "no se pudo crear el usuario"
ok "usuario creado"

TOKEN=$(curl -sf -X POST "$BASE/v1/auth/login" -H 'Content-Type: application/json' \
  -d "{\"email\":\"$EMAIL\",\"password\":\"$PASS\"}" | jsonval token)
[ -n "$TOKEN" ] || fatal "el login no devolvio token"
ok "login"

AUTH=(-H "Authorization: Bearer $TOKEN")

# /v1/auth/me cruza el gateway y resuelve la sesion en user-service.
ME=$(curl -sf "$BASE/v1/auth/me" "${AUTH[@]}" | jsonval username)
[ "$ME" = "smoke$SUF" ] || fatal "/v1/auth/me devolvio '$ME'"
ok "/v1/auth/me"

echo "== biblioteca =="
# Crear un libro exige sesion: library-service se la pregunta a user-service
# por gRPC, con el secreto interno.
BOOK=$(curl -sf -X POST "$BASE/v1/books" "${AUTH[@]}" -H 'Content-Type: application/json' \
  -d '{"title":"Prueba de humo","genres":["fantasy"]}' | jsonval id)
[ -n "$BOOK" ] || fatal "no se pudo crear el libro"
ok "libro creado"

curl -sf -X POST "$BASE/v1/books/$BOOK/chapters" "${AUTH[@]}" -H 'Content-Type: application/json' \
  -d '{"title":"Capitulo 1","content":"Texto"}' >/dev/null || fatal "no se pudo crear el capitulo"
ok "capitulo creado"

# Un libro vacio no se puede publicar; con capitulo, si.
ESTADO=$(curl -sf -X PATCH "$BASE/v1/books/$BOOK" "${AUTH[@]}" -H 'Content-Type: application/json' \
  -d '{"status":"published"}' | jsonval status)
[ "$ESTADO" = "published" ] || fatal "el libro quedo en '$ESTADO'"
ok "libro publicado"

echo "== interacciones =="
# Aqui se encadenan los tres: interaction-service le pregunta a
# library-service si el libro esta publicado, y ese le pregunta a user-service
# por el token.
curl -sf -X POST "$BASE/v1/books/$BOOK/likes" "${AUTH[@]}" >/dev/null || fatal "no se pudo dar me gusta"
ok "me gusta"

curl -sf -X POST "$BASE/v1/books/$BOOK/comments" "${AUTH[@]}" -H 'Content-Type: application/json' \
  -d '{"body":"Comentario de prueba"}' >/dev/null || fatal "no se pudo comentar"
ok "comentario"

curl -sf -X PUT "$BASE/v1/books/$BOOK/rating" "${AUTH[@]}" -H 'Content-Type: application/json' \
  -d '{"score":5}' >/dev/null || fatal "no se pudo calificar"
ok "calificacion"

RESUMEN=$(curl -sf "$BASE/v1/books/$BOOK/interactions" | tr -d '\n ')
echo "$RESUMEN" | grep -q '"count":1' || fatal "el resumen no trae los contadores: $RESUMEN"
ok "resumen de interacciones"

echo "== gateway =="
# Los metodos internos no existen desde fuera, con secreto o sin el.
CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BASE/user.v1.UserService/ValidateSession" \
  -H 'Content-Type: application/json' -d '{"token":"x"}')
[ "$CODE" = "404" ] || fatal "ValidateSession respondio $CODE desde fuera, se esperaba 404"
ok "las RPC internas no se alcanzan desde fuera"

# El catalogo de generos tiene su propia clase de rate limit (300/min): 15
# seguidas no pueden dar 429.
for _ in $(seq 1 15); do
  CODE=$(curl -s -o /dev/null -w "%{http_code}" "$BASE/v1/genres")
  [ "$CODE" = "200" ] || fatal "GET /v1/genres respondio $CODE"
done
ok "lecturas publicas dentro de su limite"

# El id de peticion vuelve al cliente: es lo que permite buscar una peticion
# concreta en el log de los tres servicios.
curl -s -i "$BASE/v1/genres" | grep -qi '^x-request-id' || fatal "la respuesta no trae x-request-id"
ok "x-request-id en la respuesta"

echo
echo "prueba de humo completa: todo respondio como se esperaba"
