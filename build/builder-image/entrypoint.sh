#!/usr/bin/env bash
# Entrypoint da imagem builder. Contrato de ambiente (injetado pelo agent em
# worker/internal/builder/docker.go):
#
#   ADILA_REPO_URL          https de clone, SEM token (ex.: https://github.com/o/r.git)
#   ADILA_GIT_TOKEN         token efêmero do GitHub App (injetado no clone; nunca logado)
#   ADILA_REF               branch ou commit a buildar
#   ADILA_COMMIT_SHA        commit exato (opcional; informativo)
#   ADILA_IMAGE_TARGET      registry/repo:tag de destino do push
#   ADILA_DOCKERFILE        caminho do Dockerfile no repo (vazio = nixpacks gera)
#   ADILA_REGISTRY_SERVER   host do registry
#   ADILA_REGISTRY_USERNAME usuário de push
#   ADILA_REGISTRY_PASSWORD senha de push
#
# Em sucesso, imprime a linha-sentinela `ADILA_IMAGE_DIGEST=sha256:...` que o agent
# parseia dos logs. Sai != 0 em qualquer falha (o agent lê isso como build FAILED).

set -euo pipefail

# Higiene de segredo: nunca expandir o token sob `set -x`; mascarar em traps.
SRC=/workspace/src

fail() {
    echo "ERRO: $*" >&2
    exit 1
}

# --- Validação mínima do contrato ---
: "${ADILA_REPO_URL:?ADILA_REPO_URL ausente}"
: "${ADILA_REF:?ADILA_REF ausente}"
: "${ADILA_IMAGE_TARGET:?ADILA_IMAGE_TARGET ausente}"

# --- Clone ---
# Injeta o token na URL só na chamada do git (não fica em env nem em log). Para repos
# públicos o token pode estar vazio — aí clona sem credencial.
echo ">> clonando ${ADILA_REPO_URL} (ref ${ADILA_REF})"
clone_url="${ADILA_REPO_URL}"
if [[ -n "${ADILA_GIT_TOKEN:-}" ]]; then
    # https://x-access-token:TOKEN@host/owner/repo.git — formato do GitHub App.
    clone_url="https://x-access-token:${ADILA_GIT_TOKEN}@${ADILA_REPO_URL#https://}"
fi

git clone --quiet --depth 1 --branch "${ADILA_REF}" "${clone_url}" "${SRC}" 2>/dev/null \
    || git clone --quiet "${clone_url}" "${SRC}" 2>/dev/null \
    || fail "git clone falhou"

# Se veio --branch e era um commit (não branch/tag), o primeiro clone falha e o
# fallback clona o default; então damos checkout no ref/commit explicitamente.
if [[ -n "${ADILA_COMMIT_SHA:-}" ]]; then
    git -C "${SRC}" checkout --quiet "${ADILA_COMMIT_SHA}" 2>/dev/null \
        || git -C "${SRC}" checkout --quiet "${ADILA_REF}" 2>/dev/null \
        || true
fi

# --- Detecta SPA estática e gera um Dockerfile padrão (servida por nginx na 8080) ---
# Quando o repo não traz Dockerfile próprio e é um front-end estático (Vite, CRA,
# Angular, Vue, Astro, Parcel...), o nixpacks falha por não achar "start command".
# Detectamos o caso pelo package.json e emitimos um Dockerfile padrão que builda os
# assets e os serve com nginx na porta 8080 (default do agent). O diretório de saída
# (dist/build/out/...) é descoberto pelo index.html após o build, não adivinhado.
# Em sucesso, ecoa no stdout o caminho do Dockerfile gerado; senão retorna != 0.
gen_spa_dockerfile() {
    local pkg="${SRC}/package.json"
    [[ -f "${pkg}" ]] || return 1
    grep -Eq '"build"[[:space:]]*:' "${pkg}" || return 1
    # "start" indica um servidor de runtime (SSR/API) — aí o nixpacks resolve melhor.
    grep -Eq '"start"[[:space:]]*:' "${pkg}" && return 1
    # Precisa de um bundler estático conhecido nas dependências.
    grep -Eq '"(vite|react-scripts|@angular/cli|@vue/cli-service|vue-cli-service|astro|parcel|@voidzero-dev/vite-plus[a-z-]*)"' "${pkg}" || return 1

    # Package manager pelo lockfile / campo packageManager (default npm).
    local base preinstall install build_cmd
    if [[ -f "${SRC}/bun.lock" || -f "${SRC}/bun.lockb" ]] \
        || grep -Eq '"packageManager"[[:space:]]*:[[:space:]]*"bun@' "${pkg}"; then
        base="oven/bun:1"; preinstall=""; install="bun install"; build_cmd="bun run build"
    elif [[ -f "${SRC}/pnpm-lock.yaml" ]]; then
        base="node:20-slim"; preinstall="RUN corepack enable"; install="pnpm install --frozen-lockfile"; build_cmd="pnpm run build"
    elif [[ -f "${SRC}/yarn.lock" ]]; then
        base="node:20-slim"; preinstall="RUN corepack enable"; install="yarn install --frozen-lockfile"; build_cmd="yarn build"
    else
        base="node:20-slim"; preinstall=""; install="npm install"; build_cmd="npm run build"
    fi

    local out="${SRC}/Dockerfile.adila-spa"
    cat > "${out}" <<'DOCKER'
# Gerado pelo Adila builder: SPA estática servida por nginx na porta 8080.
FROM __BASE__ AS build
WORKDIR /app
COPY . .
__PREINSTALL__
RUN __INSTALL__
RUN __BUILD__
# Descobre o diretório de saída estático pelo index.html e normaliza em /site.
RUN set -e; \
    for d in dist build out public .output/public dist/spa; do \
        if [ -f "$d/index.html" ]; then mkdir -p /site && cp -a "$d/." /site/ && break; fi; \
    done; \
    [ -f /site/index.html ] || { echo "ERRO: build nao gerou index.html estatico" >&2; exit 1; }

FROM nginx:1.27-alpine
COPY --from=build /site /usr/share/nginx/html
RUN printf 'server {\n  listen 8080;\n  root /usr/share/nginx/html;\n  location / { try_files $uri $uri/ /index.html; }\n}\n' > /etc/nginx/conf.d/default.conf
EXPOSE 8080
CMD ["nginx", "-g", "daemon off;"]
DOCKER
    sed -i \
        -e "s|__BASE__|${base}|" \
        -e "s|__PREINSTALL__|${preinstall}|" \
        -e "s|__INSTALL__|${install}|" \
        -e "s|__BUILD__|${build_cmd}|" \
        "${out}"
    echo "${out}"
}

# --- Resolve o Dockerfile ---
if [[ -n "${ADILA_DOCKERFILE:-}" ]]; then
    dockerfile="${SRC}/${ADILA_DOCKERFILE}"
    [[ -f "${dockerfile}" ]] || fail "Dockerfile não encontrado: ${ADILA_DOCKERFILE}"
    echo ">> usando Dockerfile do repo: ${ADILA_DOCKERFILE}"
elif [[ -f "${SRC}/Dockerfile" ]]; then
    dockerfile="${SRC}/Dockerfile"
    echo ">> usando Dockerfile do repo (raiz)"
elif spa_dockerfile=$(gen_spa_dockerfile); then
    dockerfile="${spa_dockerfile}"
    echo ">> SPA estática detectada — usando Dockerfile padrão (build + nginx :8080)"
else
    echo ">> nenhum Dockerfile e não é SPA — gerando com nixpacks"
    # --out grava .nixpacks/Dockerfile SEM buildar (kaniko builda em seguida).
    nixpacks build "${SRC}" --out "${SRC}" >/dev/null \
        || fail "nixpacks não conseguiu gerar o build para este repo"
    dockerfile="${SRC}/.nixpacks/Dockerfile"
    [[ -f "${dockerfile}" ]] || fail "nixpacks não gerou Dockerfile"
fi

# --- Auth do registry para o kaniko ---
if [[ -n "${ADILA_REGISTRY_SERVER:-}" ]]; then
    auth=$(printf '%s:%s' "${ADILA_REGISTRY_USERNAME:-}" "${ADILA_REGISTRY_PASSWORD:-}" | base64 -w0)
    mkdir -p /kaniko/.docker
    cat > /kaniko/.docker/config.json <<EOF
{"auths":{"${ADILA_REGISTRY_SERVER}":{"auth":"${auth}"}}}
EOF
fi

# --- Build + push com kaniko ---
echo ">> buildando e empurrando ${ADILA_IMAGE_TARGET}"
/kaniko/executor \
    --context "dir://${SRC}" \
    --dockerfile "${dockerfile}" \
    --destination "${ADILA_IMAGE_TARGET}" \
    --digest-file /tmp/digest \
    --snapshot-mode=redo \
    --use-new-run \
    || fail "kaniko falhou ao buildar/empurrar"

# Linha-sentinela consumida pelo agent (reDigest em docker.go).
# Lemos com o redirecionamento builtin do bash ($(< arquivo)) em vez de `cat`:
# o kaniko substitui o rootfs do container pela imagem final durante o build, então
# binários externos (cat, grep...) podem sumir (ex.: imagem final alpine/busybox).
# Builtins do bash sobrevivem porque já estão carregados no processo.
if [[ -f /tmp/digest ]]; then
    echo "ADILA_IMAGE_DIGEST=$(< /tmp/digest)"
fi
echo ">> build concluído: ${ADILA_IMAGE_TARGET}"
