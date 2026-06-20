# Imagem builder do Adila: clona um repo, gera o Dockerfile (nixpacks) ou usa o do
# repo, e builda+empurra a imagem com kaniko — tudo em USERSPACE, sem daemon Docker.
#
# O agent lança esta imagem como container efêmero e isolado (ver
# worker/internal/builder/docker.go). O container NÃO recebe o socket do Docker: o
# kaniko extrai as camadas no próprio rootfs do container, que é descartado ao fim.
#
# Build & push (rodar numa máquina com Docker, uma vez por release):
#   docker build -t registry-dash.adila.co/adila/builder:latest worker/build/builder-image
#   docker push registry-dash.adila.co/adila/builder:latest
# e aponte AGENT_BUILDER_IMAGE para essa tag na box do agent.
# (Setup do registry: ver worker/deploy/registry.)

# Binário estático do kaniko, copiado para uma base com shell + git + nixpacks.
FROM gcr.io/kaniko-project/executor:v1.23.2 AS kaniko

FROM debian:bookworm-slim

# git (clone), ca-certificates (TLS no clone/push), curl + xz (instalar nixpacks).
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        git ca-certificates curl xz-utils \
    && rm -rf /var/lib/apt/lists/*

# nixpacks: gera o Dockerfile a partir do código (autodetecta linguagem/framework).
# Usamos `nixpacks build --out` (não builda; só emite o Dockerfile) para o kaniko buildar.
ARG NIXPACKS_VERSION=1.29.1
RUN curl -sSL https://nixpacks.com/install.sh | bash -s -- --version "${NIXPACKS_VERSION}"

# Executor do kaniko + os diretórios que ele espera (config docker e warmer cache).
COPY --from=kaniko /kaniko/executor /kaniko/executor
RUN mkdir -p /kaniko/.docker /workspace
ENV DOCKER_CONFIG=/kaniko/.docker
ENV PATH="/kaniko:${PATH}"

COPY entrypoint.sh /usr/local/bin/adila-build
RUN chmod +x /usr/local/bin/adila-build

# Roda como root: o kaniko precisa para extrair camadas no rootfs. O isolamento vem
# do container ser efêmero e SEM acesso ao daemon/host (garantido pelo agent).
ENTRYPOINT ["/usr/local/bin/adila-build"]
