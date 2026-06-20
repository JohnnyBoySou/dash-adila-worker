# Registry privado (`registry:2` + Caddy) — Fase 0

Registry OCI privado que roda **na box Hetzner**, para onde o kaniko (imagem
builder) empurra as imagens e de onde o agent as puxa no deploy. O Caddy termina
TLS automaticamente (Let's Encrypt); o registry valida a autenticação (htpasswd).

```
kaniko (container builder)  --push-->  ┌─────────────────────────────┐
                                       │  Caddy :443  (auto-TLS)      │
agent (docker pull no deploy) --pull-->│     └─> registry:5000        │
                                       │           (htpasswd bcrypt)  │
                                       └─────────────────────────────┘
```

## Por que assim

- **TLS de verdade, sem cargo-cult**: o Caddy obtém e renova o cert do domínio
  público sozinho. O kaniko empurra por HTTPS válido — **nunca** usamos
  `--skip-tls-verify`.
- **Auth obrigatória**: htpasswd (bcrypt, o único formato aceito pelo
  `registry:2`). Sem push/pull anônimo.
- **Registry nunca exposto direto**: no `docker-compose.yml` o serviço `registry`
  não tem `ports:`. Só o Caddy, na rede interna do compose, o alcança — toda
  entrada passa por TLS + auth.

## Pré-requisitos

1. DNS: `REGISTRY_DOMAIN` (ex.: `registry-dash.adila.co`) resolvendo para o IP da
   box.
2. Firewall: portas **80** e **443** acessíveis a partir da internet (o ACME usa
   80/443 para emitir o cert; os clientes usam 443).
3. Docker + Docker Compose na box.

## Subir

```bash
cd worker/deploy/registry

cp .env.example .env
# edite REGISTRY_DOMAIN, ACME_EMAIL e gere o REGISTRY_HTTP_SECRET:
#   openssl rand -hex 32

# cria auth/htpasswd e imprime o usuário/senha p/ o control plane:
./gen-htpasswd.sh adila-ci

docker compose up -d
docker compose logs -f caddy   # acompanhe a emissão do cert
```

Valide:

```bash
# precisa de 401 sem credencial e 200 com ela:
curl -fsS https://$REGISTRY_DOMAIN/v2/ -o /dev/null -w '%{http_code}\n'        # 401
curl -fsS -u adila-ci:<senha> https://$REGISTRY_DOMAIN/v2/ -w '%{http_code}\n' # 200
```

## Publicar a imagem builder aqui

A imagem `adila-builder` (ver `worker/build/builder-image`) pode morar neste
registry. Numa máquina com Docker, uma vez por release:

```bash
docker login $REGISTRY_DOMAIN -u adila-ci
docker build -t $REGISTRY_DOMAIN/adila/builder:latest worker/build/builder-image
docker push  $REGISTRY_DOMAIN/adila/builder:latest
```

Na box do agent, aponte a env:

```bash
AGENT_BUILDER_IMAGE=registry-dash.adila.co/adila/builder:latest
```

## Wiring com o control plane (`back/`)

O control plane lê as credenciais de push do `.env` e as repassa ao agent, que as
injeta no `config.json` do kaniko (efêmero, descartado ao fim do build). Adicione
ao `back/.env`:

```bash
# Host do registry (bare host, SEM esquema). Como o Caddy serve em 443, não há
# porta no fim — este valor entra tanto no ADILA_IMAGE_TARGET quanto na chave do
# config.json do kaniko, então precisa bater com o host do domínio.
REGISTRY_URL=registry-dash.adila.co
REGISTRY_USERNAME=adila-ci
REGISTRY_PASSWORD=<senha impressa pelo gen-htpasswd.sh>
```

> O `provisioning.ts` monta o alvo como `${REGISTRY_URL}/${service.id}:${tag}`.

## Garbage collection

Tags reescritas deixam blobs órfãos. Rode periodicamente (com o registry parado ou
em modo read-only para consistência):

```bash
docker compose exec registry \
  bin/registry garbage-collect /etc/docker/registry/config.yml --delete-untagged
```

## Sobre o hostname `registry.interno:5000`

Esse nome **não** é compatível com este caminho (Caddy auto-TLS): o Let's Encrypt
só emite certificado para um FQDN público resolvível em 80/443, não para um nome
privado em porta custom — e o kaniko exige TLS válido. Para usar um nome interno
sem exposição pública seria preciso a variante **CA interna** (cert self-signed
montado no registry e confiado pela imagem builder + dockerd da box), que é outro
desenho. Por isso o endpoint canônico aqui é o subdomínio público.

## Segurança

- `.env` e `auth/` estão no `.gitignore` — segredos nunca vão para o git.
- O `REGISTRY_PASSWORD` em claro só existe no `back/.env`; o registry guarda apenas
  o hash bcrypt.
- O registry não escuta na rede pública; o Caddy é a única porta de entrada.
