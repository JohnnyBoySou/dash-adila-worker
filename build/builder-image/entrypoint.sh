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

# --- Resolve o Dockerfile ---
if [[ -n "${ADILA_DOCKERFILE:-}" ]]; then
    dockerfile="${SRC}/${ADILA_DOCKERFILE}"
    [[ -f "${dockerfile}" ]] || fail "Dockerfile não encontrado: ${ADILA_DOCKERFILE}"
    echo ">> usando Dockerfile do repo: ${ADILA_DOCKERFILE}"
elif [[ -f "${SRC}/Dockerfile" ]]; then
    dockerfile="${SRC}/Dockerfile"
    echo ">> usando Dockerfile do repo (raiz)"
else
    echo ">> nenhum Dockerfile — gerando com nixpacks"
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
if [[ -f /tmp/digest ]]; then
    echo "ADILA_IMAGE_DIGEST=$(cat /tmp/digest)"
fi
echo ">> build concluído: ${ADILA_IMAGE_TARGET}"
