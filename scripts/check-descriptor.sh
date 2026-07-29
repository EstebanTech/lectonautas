#!/usr/bin/env bash
# Comprueba que gateway/proto/services.pb está al día con los .proto.
#
# El descriptor es un binario versionado que hay que regenerar a mano
# (`make proto-descriptor`), y olvidarlo tiene un síntoma feo: el gateway sigue
# arrancando, pero el método nuevo responde 404 —o el campo nuevo hace que el
# transcoder rechace el JSON— sin que nada diga por qué.
#
# No se regenera para comparar byte a byte a propósito: la salida de protoc
# depende de su versión, así que esa comparación fallaría por motivos que no
# tienen nada que ver con el contrato. Lo que se comprueba es la propiedad que
# de verdad importa: que cada rpc y cada campo declarado en los .proto esté
# dentro del descriptor. Los nombres viajan como texto plano en un
# FileDescriptorSet, así que basta con buscarlos.
#
# Es una comprobación en un solo sentido (detecta lo que falta, no lo que
# sobra), que es justo el fallo que ocurre en la práctica.
set -euo pipefail

cd "$(dirname "$0")/.."

DESCRIPTOR=gateway/proto/services.pb
PROTOS=(
  backend/microservices/user-service/proto/user/v1/user.proto
  backend/microservices/library-service/proto/library/v1/library.proto
  backend/microservices/interaction-service/proto/interaction/v1/interaction.proto
)

if [ ! -f "$DESCRIPTOR" ]; then
  echo "no existe $DESCRIPTOR: generalo con 'make proto-descriptor' desde la raiz" >&2
  exit 1
fi

faltan=0

comprobar() {
  local nombre="$1" origen="$2" tipo="$3"
  if ! grep -qa -- "$nombre" "$DESCRIPTOR"; then
    echo "FALTA en el descriptor: $tipo $nombre (declarado en $origen)" >&2
    faltan=1
  fi
}

for proto in "${PROTOS[@]}"; do
  # rpc NombreDelMetodo(...)
  while read -r rpc; do
    [ -n "$rpc" ] && comprobar "$rpc" "$proto" "rpc"
  done < <(grep -oE '^[[:space:]]*rpc[[:space:]]+[A-Za-z0-9_]+' "$proto" | awk '{print $2}')

  # message NombreDelMensaje {
  while read -r msg; do
    [ -n "$msg" ] && comprobar "$msg" "$proto" "message"
  done < <(grep -oE '^[[:space:]]*message[[:space:]]+[A-Za-z0-9_]+' "$proto" | awk '{print $2}')

  # nombre_de_campo = N;  (incluye repeated/optional y tipos con punto)
  while read -r campo; do
    [ -n "$campo" ] && comprobar "$campo" "$proto" "campo"
  done < <(grep -oE '^[[:space:]]*(repeated[[:space:]]+|optional[[:space:]]+)?[A-Za-z0-9_.]+[[:space:]]+[a-z][a-z0-9_]*[[:space:]]*=[[:space:]]*[0-9]+' "$proto" \
            | sed -E 's/[[:space:]]*=[[:space:]]*[0-9]+$//' | awk '{print $NF}' | sort -u)
done

if [ "$faltan" -ne 0 ]; then
  echo >&2
  echo "El descriptor del gateway no esta al dia. Desde la raiz:" >&2
  echo "  make proto-descriptor && docker compose restart gateway" >&2
  exit 1
fi

echo "descriptor al dia: los rpc, mensajes y campos de los .proto estan dentro"
