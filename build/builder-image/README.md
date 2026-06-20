# Imagem builder (`adila-builder`)

Container efêmero que o agent lança em `POST /v1/builds` para transformar um repo
git numa imagem OCI, **sem daemon Docker** — `nixpacks` gera o Dockerfile (ou usa o
do repo) e `kaniko` builda e empurra em userspace.

## Por que assim

- **Isolamento multi-tenant**: o build roda num container descartável e **sem o
  socket do Docker montado**. Código de tenant não-confiável nunca alcança o daemon
  nem outros containers. O kaniko extrai as camadas no rootfs do próprio container.
- **Sem build box dedicada**: reaproveita a box do agent; o container some ao fim.

## Build e publicação

Numa máquina com Docker (uma vez por release da imagem builder):

```bash
docker build -t registry.interno:5000/adila/builder:latest worker/build/builder-image
docker push  registry.interno:5000/adila/builder:latest
```

Na box do agent, aponte a env para essa tag:

```bash
AGENT_BUILDER_IMAGE=registry.interno:5000/adila/builder:latest
```

(Default se omitido: `adila-builder:latest`.)

## Contrato de ambiente

O agent injeta estas variáveis no `docker run` do container (ver
`worker/internal/builder/docker.go`):

| Env | Origem | Obrigatório |
|-----|--------|-------------|
| `ADILA_REPO_URL` | URL https de clone, **sem** token | sim |
| `ADILA_GIT_TOKEN` | token efêmero do GitHub App (injetado no clone; nunca logado) | não (repo público) |
| `ADILA_REF` | branch ou commit | sim |
| `ADILA_COMMIT_SHA` | commit exato (checkout) | não |
| `ADILA_IMAGE_TARGET` | `registry/repo:tag` de destino | sim |
| `ADILA_DOCKERFILE` | caminho do Dockerfile no repo (vazio = nixpacks) | não |
| `ADILA_REGISTRY_SERVER/USERNAME/PASSWORD` | credenciais de push | sim (registry privado) |

Em sucesso, o entrypoint imprime `ADILA_IMAGE_DIGEST=sha256:...`, que o agent
parseia dos logs (`reDigest`). Saída != 0 → o agent reporta o build como `failed`.

## Notas de segurança

- O token git é injetado **apenas** na URL de clone, em runtime — não fica em env
  persistente nem aparece nos logs.
- As credenciais do registry viram `/kaniko/.docker/config.json` dentro do container
  efêmero, descartado ao fim.
- Sem `--privileged` e sem `/var/run/docker.sock` — o agent garante isso.
