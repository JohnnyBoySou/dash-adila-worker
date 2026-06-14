package runtime

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Prefixo dos labels que marcam containers gerenciados por este agent. Os labels
// são a ÚNICA fonte de estado: o agent não guarda nada em memória/disco, então
// sobrevive a restart e reflete sempre o que o Docker realmente tem.
const labelPrefix = "adila."

// Porta interna do Postgres e do Redis dentro do container.
const pgContainerPort = "5432/tcp"
const redisContainerPort = "6379/tcp"

// DockerConfig parametriza a implementação Docker.
type DockerConfig struct {
	// Bin é o binário do Docker (default "docker").
	Bin string
	// BindHost é o IP do host onde a porta do container é publicada.
	// Local: 127.0.0.1. Produção: o IP da rede privada da box.
	BindHost string
	// PortRangeStart/PortRangeEnd: range de portas para Postgres (default 20000-29999).
	PortRangeStart int
	PortRangeEnd   int
	// RedisPortRangeStart/RedisPortRangeEnd: range separado para Redis (default 30000-39999).
	// Range separado evita colisão entre os dois tipos no mesmo host.
	RedisPortRangeStart int
	RedisPortRangeEnd   int
	// AppPortRangeStart/AppPortRangeEnd: range separado para apps (default 40000-49999).
	AppPortRangeStart int
	AppPortRangeEnd   int
}

// Docker implementa ContainerRuntime acionando o CLI `docker` via os/exec.
//
// Por que CLI e não o SDK oficial: zero dependências externas (go.mod limpo, menos
// superfície de supply chain) e transparência total. Os argumentos são passados como
// slice (NUNCA via shell), então não há injeção de shell.
type Docker struct {
	cfg DockerConfig
	// mu serializa a seção crítica do Create (procurar-ou-criar) para evitar que dois
	// provisions concorrentes com a mesma idempotencyKey criem containers duplicados.
	mu sync.Mutex
}

func NewDocker(cfg DockerConfig) *Docker {
	if cfg.Bin == "" {
		cfg.Bin = "docker"
	}
	if cfg.BindHost == "" {
		cfg.BindHost = "127.0.0.1"
	}
	if cfg.PortRangeStart == 0 {
		cfg.PortRangeStart = 20000
	}
	if cfg.PortRangeEnd == 0 {
		cfg.PortRangeEnd = 29999
	}
	if cfg.RedisPortRangeStart == 0 {
		cfg.RedisPortRangeStart = 30000
	}
	if cfg.RedisPortRangeEnd == 0 {
		cfg.RedisPortRangeEnd = 39999
	}
	if cfg.AppPortRangeStart == 0 {
		cfg.AppPortRangeStart = 40000
	}
	if cfg.AppPortRangeEnd == 0 {
		cfg.AppPortRangeEnd = 49999
	}
	return &Docker{cfg: cfg}
}

func (d *Docker) Create(ctx context.Context, spec Spec) (*Instance, error) {
	switch spec.Kind {
	case KindPostgres:
		return d.createForKind(ctx, spec, d.createOrFindPostgres)
	case KindRedis:
		return d.createForKind(ctx, spec, d.createOrFindRedis)
	case KindApp:
		return d.createForKind(ctx, spec, d.createOrFindApp)
	default:
		return nil, fmt.Errorf("kind desconhecido: %s", spec.Kind)
	}
}

// createForKind executa o padrão find-or-create com read-back fora do lock.
// Separa a lógica de despacho da lógica de criação para reusar com Redis e Postgres.
func (d *Docker) createForKind(
	ctx context.Context,
	spec Spec,
	createFn func(context.Context, Spec) (*Instance, error),
) (*Instance, error) {
	existing, err := createFn(ctx, spec)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil // idempotência: container já existia
	}
	// Read-back FORA do lock: Get só relê do Docker (não toca estado em memória),
	// então não precisa da seção crítica e não a prende durante o inspect.
	return d.Get(ctx, spec.ID)
}

// createOrFindPostgres é a seção crítica serializada para Postgres.
// Devolve o container existente se a idempotencyKey já bate; (nil,nil) quando acabou
// de criar — nesse caso quem chama faz o read-back fora do lock.
func (d *Docker) createOrFindPostgres(ctx context.Context, spec Spec) (*Instance, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if existing, err := d.findByKey(ctx, spec.IdempotencyKey); err != nil {
		return nil, err
	} else if existing != nil {
		return existing, nil
	}

	password, err := randHex(24)
	if err != nil {
		return nil, fmt.Errorf("gerar senha: %w", err)
	}
	// Porta FIXA por tenant (escolhida sob o lock, gravada em label): estável através
	// de stop/start, então a connection URL não fica stale após suspend/resume.
	port, err := d.allocatePort(ctx, KindPostgres)
	if err != nil {
		return nil, err
	}
	const user, db = "app", "app"
	name := containerName(spec.ID)
	volume := volumeName(spec.ID)

	args := []string{
		"run", "-d",
		"--name", name,
		// on-failure recupera crash transitório (até 3x) mas deixa um crash persistente
		// virar "exited != 0" → detectável como crashed.
		"--restart", "on-failure:3",
		"--label", label("managed", "true"),
		"--label", label("id", spec.ID),
		"--label", label("idempotency", spec.IdempotencyKey),
		"--label", label("kind", string(spec.Kind)),
		"--label", label("name", spec.Name),
		"--label", label("region", spec.Region),
		"--label", label("pg.user", user),
		"--label", label("pg.password", password),
		"--label", label("pg.db", db),
		// hostport gravado em label = fonte de verdade da porta (lida mesmo com o
		// container parado, quando NetworkSettings.Ports vem vazio).
		"--label", label("pg.hostport", strconv.Itoa(port)),
		"-e", "POSTGRES_USER=" + user,
		"-e", "POSTGRES_PASSWORD=" + password,
		"-e", "POSTGRES_DB=" + db,
		"-p", fmt.Sprintf("%s:%d:5432", d.cfg.BindHost, port),
		"-v", volume + ":/var/lib/postgresql/data",
		"--health-cmd", "pg_isready -U " + user + " -d " + db,
		"--health-interval=2s",
		"--health-timeout=3s",
		"--health-retries=10",
		"--health-start-period=3s",
	}
	if spec.MemoryMb > 0 {
		args = append(args, "--memory", fmt.Sprintf("%dm", spec.MemoryMb))
	}
	if spec.CPUs > 0 {
		args = append(args, "--cpus", strconv.FormatFloat(spec.CPUs, 'f', -1, 64))
	}
	args = append(args, "postgres:"+spec.Version)

	if _, err := d.run(ctx, args...); err != nil {
		return nil, fmt.Errorf("docker run postgres: %w", err)
	}
	return nil, nil
}

// createOrFindRedis é a seção crítica serializada para Redis.
// Mesmo padrão que createOrFindPostgres, mas com labels/portas/imagem de Redis.
func (d *Docker) createOrFindRedis(ctx context.Context, spec Spec) (*Instance, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if existing, err := d.findByKey(ctx, spec.IdempotencyKey); err != nil {
		return nil, err
	} else if existing != nil {
		return existing, nil
	}

	// Senha gerada via randHex → apenas chars [0-9a-f], então segura em
	// --health-cmd (sem injeção de shell possível) e no redis-server --requirepass.
	password, err := randHex(24)
	if err != nil {
		return nil, fmt.Errorf("gerar senha Redis: %w", err)
	}
	port, err := d.allocatePort(ctx, KindRedis)
	if err != nil {
		return nil, err
	}
	name := containerName(spec.ID)
	volume := volumeName(spec.ID)

	args := []string{
		"run", "-d",
		"--name", name,
		"--restart", "on-failure:3",
		"--label", label("managed", "true"),
		"--label", label("id", spec.ID),
		"--label", label("idempotency", spec.IdempotencyKey),
		"--label", label("kind", string(spec.Kind)),
		"--label", label("name", spec.Name),
		"--label", label("region", spec.Region),
		"--label", label("redis.password", password),
		"--label", label("redis.hostport", strconv.Itoa(port)),
		"-p", fmt.Sprintf("%s:%d:6379", d.cfg.BindHost, port),
		"-v", volume + ":/data",
		"--health-cmd", fmt.Sprintf(
			"redis-cli -a %s --no-auth-warning ping", password),
		"--health-interval=2s",
		"--health-timeout=3s",
		"--health-retries=10",
		"--health-start-period=3s",
	}
	if spec.MemoryMb > 0 {
		args = append(args, "--memory", fmt.Sprintf("%dm", spec.MemoryMb))
	}
	if spec.CPUs > 0 {
		args = append(args, "--cpus", strconv.FormatFloat(spec.CPUs, 'f', -1, 64))
	}
	// Imagem + comando: redis-server com autenticação obrigatória e AOF habilitado
	// para persistência (dump.rdb continua sendo gerado para backups).
	args = append(args, "redis:"+spec.Version,
		"redis-server", "--requirepass", password, "--appendonly", "yes")

	if _, err := d.run(ctx, args...); err != nil {
		return nil, fmt.Errorf("docker run redis: %w", err)
	}
	return nil, nil
}

func (d *Docker) Get(ctx context.Context, id string) (*Instance, error) {
	insp, err := d.inspect(ctx, containerName(id))
	if err != nil {
		return nil, err
	}
	if insp == nil {
		return nil, nil
	}
	return buildInstance(insp), nil
}

func (d *Docker) Stop(ctx context.Context, id string) error {
	if _, err := d.run(ctx, "stop", containerName(id)); err != nil {
		if isNoSuchObject(err) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

func (d *Docker) Start(ctx context.Context, id string) error {
	if _, err := d.run(ctx, "start", containerName(id)); err != nil {
		if isNoSuchObject(err) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

func (d *Docker) Delete(ctx context.Context, id string, destroyData bool) error {
	// rm -f derruba mesmo se estiver rodando. Não-existe → idempotente (não é erro).
	if _, err := d.run(ctx, "rm", "-f", containerName(id)); err != nil && !isNoSuchObject(err) {
		return err
	}
	if destroyData {
		// O volume é nomeado, então `rm -v` não o remove — apaga-se explicitamente.
		if _, err := d.run(ctx, "volume", "rm", volumeName(id)); err != nil && !isNoSuchObject(err) {
			return err
		}
	}
	return nil
}

// ListRunning devolve todas as instâncias de containers gerenciados com status
// running. Usado pelo idle-stop (decidir o que parar) e pelo backup (o que backupear).
// Melhor esforço: containers que falham no inspect individual são ignorados.
func (d *Docker) ListRunning(ctx context.Context) ([]*Instance, error) {
	out, err := d.run(ctx, "ps", "-q",
		"--filter", "label="+labelPrefix+"managed=true",
		"--filter", "status=running")
	if err != nil {
		return nil, fmt.Errorf("listar containers rodando: %w", err)
	}
	var result []*Instance
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		cid := strings.TrimSpace(line)
		if cid == "" {
			continue
		}
		insp, err := d.inspect(ctx, cid)
		if err != nil || insp == nil {
			continue // melhor esforço: ignora containers que falham no inspect
		}
		result = append(result, buildInstance(insp))
	}
	return result, nil
}

// findByKey localiza um container gerenciado pela idempotencyKey via filtro de label.
func (d *Docker) findByKey(ctx context.Context, key string) (*Instance, error) {
	out, err := d.run(ctx, "ps", "-aq", "--filter", "label="+labelPrefix+"idempotency="+key)
	if err != nil {
		return nil, err
	}
	cid := strings.TrimSpace(out)
	if cid == "" {
		return nil, nil
	}
	cid = strings.SplitN(cid, "\n", 2)[0]
	insp, err := d.inspect(ctx, cid)
	if err != nil || insp == nil {
		return nil, err
	}
	return buildInstance(insp), nil
}

// allocatePort escolhe uma porta de host livre dentro do range configurado para o
// kind dado. Deve rodar sob o lock do createOrFind correspondente. A reserva é por
// LABEL (não pelo bind físico) — um container parado mantém a porta reservada.
func (d *Docker) allocatePort(ctx context.Context, kind Kind) (int, error) {
	var labelKey string
	var start, end int
	switch kind {
	case KindRedis:
		labelKey = "redis.hostport"
		start = d.cfg.RedisPortRangeStart
		end = d.cfg.RedisPortRangeEnd
	case KindApp:
		labelKey = "app.hostport"
		start = d.cfg.AppPortRangeStart
		end = d.cfg.AppPortRangeEnd
	default: // KindPostgres
		labelKey = "pg.hostport"
		start = d.cfg.PortRangeStart
		end = d.cfg.PortRangeEnd
	}
	used, err := d.usedPorts(ctx, labelKey)
	if err != nil {
		return 0, err
	}
	return pickFreePort(used, start, end)
}

// usedPorts coleta as portas já reservadas, lendo o label especificado de todos os
// containers gerenciados (rodando ou parados).
func (d *Docker) usedPorts(ctx context.Context, hostportLabel string) (map[int]bool, error) {
	format := `{{.Label "` + labelPrefix + hostportLabel + `"}}`
	out, err := d.run(ctx, "ps", "-a",
		"--filter", "label="+labelPrefix+"managed=true",
		"--format", format)
	if err != nil {
		return nil, fmt.Errorf("listar portas em uso: %w", err)
	}
	used := make(map[int]bool)
	for _, line := range strings.Split(out, "\n") {
		if p, err := strconv.Atoi(strings.TrimSpace(line)); err == nil && p > 0 {
			used[p] = true
		}
	}
	return used, nil
}

// pickFreePort é a política pura de seleção: a menor porta do range [start,end]
// que não está em uso. Separada do I/O para ser testável sem Docker.
func pickFreePort(used map[int]bool, start, end int) (int, error) {
	for p := start; p <= end; p++ {
		if !used[p] {
			return p, nil
		}
	}
	return 0, fmt.Errorf("range de portas esgotado (%d-%d): sem porta livre", start, end)
}

// inspect roda `docker inspect <ref>` e devolve o estado parseado, ou (nil,nil) se
// o objeto não existe.
func (d *Docker) inspect(ctx context.Context, ref string) (*dockerInspect, error) {
	out, err := d.run(ctx, "inspect", ref)
	if err != nil {
		if isNoSuchObject(err) {
			return nil, nil
		}
		return nil, err
	}
	var arr []dockerInspect
	if err := json.Unmarshal([]byte(out), &arr); err != nil {
		return nil, fmt.Errorf("parse docker inspect: %w", err)
	}
	if len(arr) == 0 {
		return nil, nil
	}
	return &arr[0], nil
}

// run executa o binário do Docker com os args dados (sem shell) e captura stdout/stderr.
func (d *Docker) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, d.cfg.Bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// --- helpers ---

// portBinding é um bind publicado de uma porta do container (HostIp:HostPort).
type portBinding struct {
	HostIP   string `json:"HostIp"`
	HostPort string `json:"HostPort"`
}

// dockerInspect é o subconjunto da saída de `docker inspect` que o agent consome.
type dockerInspect struct {
	State struct {
		Status    string `json:"Status"`
		ExitCode  int    `json:"ExitCode"`
		StartedAt string `json:"StartedAt"` // RFC3339Nano — zero value: "0001-01-01T00:00:00Z"
		Health    *struct {
			Status string `json:"Status"`
		} `json:"Health"`
	} `json:"State"`
	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	NetworkSettings struct {
		Ports map[string][]portBinding `json:"Ports"`
	} `json:"NetworkSettings"`
}

func buildInstance(insp *dockerInspect) *Instance {
	labels := insp.Config.Labels
	kind := Kind(labels[labelPrefix+"kind"])
	health := ""
	if insp.State.Health != nil {
		health = insp.State.Health.Status
	}

	var hostPort int
	var user, password, database string

	switch kind {
	case KindRedis:
		hostPort = instanceHostPortFromLabel(labels, "redis.hostport",
			insp.NetworkSettings.Ports[redisContainerPort])
		password = labels[labelPrefix+"redis.password"]
		// Redis não tem user nem database nomeado (usa índice 0).
	case KindApp:
		// Porta lida do label gravado em criação (persiste mesmo com container parado).
		hostPort = instanceHostPortFromLabel(labels, "app.hostport", nil)
	default: // KindPostgres (e qualquer kind legado desconhecido)
		hostPort = instanceHostPortFromLabel(labels, "pg.hostport",
			insp.NetworkSettings.Ports[pgContainerPort])
		user = labels[labelPrefix+"pg.user"]
		password = labels[labelPrefix+"pg.password"]
		database = labels[labelPrefix+"pg.db"]
	}

	var startedAt time.Time
	if t, err := time.Parse(time.RFC3339Nano, insp.State.StartedAt); err == nil && !t.IsZero() {
		startedAt = t
	}

	return &Instance{
		ID:             labels[labelPrefix+"id"],
		IdempotencyKey: labels[labelPrefix+"idempotency"],
		Kind:           kind,
		Name:           labels[labelPrefix+"name"],
		Region:         labels[labelPrefix+"region"],
		Status:         mapDockerState(insp.State.Status, insp.State.ExitCode, health),
		HostPort:       hostPort,
		User:           user,
		Password:       password,
		Database:       database,
		StartedAt:      startedAt,
	}
}

// instanceHostPortFromLabel resolve a porta de host do container. Fonte primária =
// o label adila.<labelKey> (a porta FIXA reservada por tenant), que persiste mesmo
// com o container parado — quando NetworkSettings.Ports vem vazio. Fallback para o
// bind ativo cobre container legado sem o label.
func instanceHostPortFromLabel(labels map[string]string, labelKey string, bindings []portBinding) int {
	if p, err := strconv.Atoi(labels[labelPrefix+labelKey]); err == nil && p > 0 {
		return p
	}
	return parseHostPort(bindings)
}

func parseHostPort(bindings []portBinding) int {
	for _, b := range bindings {
		if p, err := strconv.Atoi(b.HostPort); err == nil && p > 0 {
			return p
		}
	}
	return 0
}

// ContainerName devolve o nome do container Docker para o ID de recurso dado.
// Exportado para que os pacotes backup e idlestop possam usar sem importar a lógica
// de criação.
func ContainerName(id string) string { return containerName(id) }

func containerName(id string) string { return "adila-" + id }
func volumeName(id string) string    { return "adila-" + id + "-data" }
func label(key, value string) string { return labelPrefix + key + "=" + value }

// isNoSuchObject reconhece o erro do Docker para objeto inexistente (container/volume),
// usado para tratar 404/idempotência. Ancorado nas mensagens canônicas do Docker
// ("No such container/volume/object") em vez de só "no such".
func isNoSuchObject(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such container") ||
		strings.Contains(msg, "no such volume") ||
		strings.Contains(msg, "no such object")
}

func randHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
