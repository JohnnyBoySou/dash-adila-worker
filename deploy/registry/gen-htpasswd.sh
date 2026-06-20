#!/usr/bin/env bash
# Gera/atualiza o htpasswd (bcrypt) do registry.
#
# Uso:
#   ./gen-htpasswd.sh <usuario> [<senha>]
#
# Sem senha, gera uma forte e a imprime no fim — guarde-a como REGISTRY_PASSWORD
# no .env do control plane (back/) e nada mais (o registry só guarda o hash).
#
# bcrypt (-B) é o ÚNICO formato de htpasswd aceito pelo registry:2.
set -euo pipefail

user="${1:?uso: ./gen-htpasswd.sh <usuario> [<senha>]}"
pass="${2:-$(openssl rand -base64 24)}"

dir="$(cd "$(dirname "$0")" && pwd)"
mkdir -p "${dir}/auth"

# Usa a imagem httpd só para obter o binário htpasswd (não exige apache2-utils local).
docker run --rm --entrypoint htpasswd httpd:2 -Bbn "${user}" "${pass}" \
	> "${dir}/auth/htpasswd"

echo "htpasswd escrito em ${dir}/auth/htpasswd"
echo
echo "Defina no .env do control plane (back/):"
echo "  REGISTRY_USERNAME=${user}"
echo "  REGISTRY_PASSWORD=${pass}"
