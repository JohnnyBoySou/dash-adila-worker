package builder

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// Imagem builder padrão: contém git + nixpacks + kaniko e um entrypoint que faz
// clone → nixpacks/Dockerfile → kaniko build+push. Ver build/builder-image/ para
// a definição. Sobrescrevível por AGENT_BUILDER_IMAGE.
const defaultBuilderImage = "adila-builder:latest"

// Prefixo dos labels do container de build. NÃO usa adila.managed=true de propósito:
// os labels managed são varridos por idle-stop e backup, que parariam/processariam
// um build em andamento. O build se identifica por adila.build=true à parte.
const labelPrefix = "adila."

// reDigest casa a linha-sentinela que o entrypoint do builder imprime no fim do push
// (ADILA_IMAGE_DIGEST=sha256:...), de onde lemos o digest sem consultar o registry.
var reDigest = regexp.MustCompile(`(?m)^ADILA_IMAGE_DIGEST=(sha256:[a-f0-9]{64})\s*$`)

// DockerConfig parametriza a implementação Docker do builder.
type DockerConfig struct {
	// Bin é o binário do Docker (default "docker").
	Bin string
	// BuilderImage é a imagem do container de build (default defaultBuilderImage).
	BuilderImage string
}

// Docker implementa Builder lançando um container kaniko via CLI do `docker`.
//
// Isolamento: o container de build roda SEM montar o socket do Docker. O kaniko
// builda em userspace (sem daemon), então o código do tenant nunca alcança o daemon
// nem outros containers. Os args são passados como slice (NUNCA via shell) — sem
// injeção. O daemon do host só é usado para LANÇAR o container kaniko, não fica
// acessível de dentro dele.
type Docker struct {
	cfg DockerConfig
}

func NewDocker(cfg DockerConfig) *Docker {
	if cfg.Bin == "" {
		cfg.Bin = "docker"
	}
	if cfg.BuilderImage == "" {
		cfg.BuilderImage = defaultBuilderImage
	}
	return &Docker{cfg: cfg}
}

func (d *Docker) Start(ctx context.Context, spec Spec) error {
	name := buildContainerName(spec.ID)

	args := []string{
		"run", "-d",
		"--name", name,
		// Falha de build NÃO deve reiniciar: o container exited (code != 0) É o sinal
		// de "build falhou" que o Get lê. on-failure reiniciaria e mascararia isso.
		"--restart", "no",
		"--label", label("build", "true"),
		"--label", label("kind", "build"),
		"--label", label("id", spec.ID),
		"--label", label("idempotency", spec.IdempotencyKey),
		"--label", label("name", spec.Name),
		// Guardado em label para o Get remontar o imageRef sem reparsear logs.
		"--label", label("image.target", spec.ImageTarget),
		// Contrato de env com o entrypoint do builder. exec por slice torna "KEY=VALUE"
		// um único argumento (sem injeção). Segredos (token git, senha do registry) vão
		// por env, consistente com como o agent já injeta senhas em kind=app/postgres —
		// o host é confiável (só o agent roda nele).
		"-e", "ADILA_REPO_URL=" + spec.RepoCloneURL,
		"-e", "ADILA_GIT_TOKEN=" + spec.GitToken,
		"-e", "ADILA_REF=" + spec.Ref,
		"-e", "ADILA_IMAGE_TARGET=" + spec.ImageTarget,
		"-e", "ADILA_REGISTRY_SERVER=" + spec.RegistryAuth.Server,
		"-e", "ADILA_REGISTRY_USERNAME=" + spec.RegistryAuth.Username,
		"-e", "ADILA_REGISTRY_PASSWORD=" + spec.RegistryAuth.Password,
	}
	if spec.CommitSha != "" {
		args = append(args, "-e", "ADILA_COMMIT_SHA="+spec.CommitSha)
	}
	if spec.Dockerfile != "" {
		args = append(args, "-e", "ADILA_DOCKERFILE="+spec.Dockerfile)
	}
	if spec.MemoryMb > 0 {
		args = append(args, "--memory", fmt.Sprintf("%dm", spec.MemoryMb))
	}
	if spec.CPUs > 0 {
		args = append(args, "--cpus", strconv.FormatFloat(spec.CPUs, 'f', -1, 64))
	}
	args = append(args, d.cfg.BuilderImage)

	if _, err := d.run(ctx, args...); err != nil {
		return fmt.Errorf("docker run build: %w", err)
	}
	return nil
}

func (d *Docker) Get(ctx context.Context, id string) (*Status, error) {
	name := buildContainerName(id)
	insp, err := d.inspect(ctx, name)
	if err != nil {
		return nil, err
	}
	if insp == nil {
		return nil, nil
	}
	status := mapBuildState(insp.State.Status, insp.State.ExitCode)
	// Logs são best-effort: a falha de coleta não invalida o status.
	logs, _ := d.run(ctx, "logs", name)

	st := &Status{
		ID:       id,
		Status:   status,
		ImageRef: insp.Config.Labels[labelPrefix+"image.target"],
		Logs:     logs,
	}
	// ImageRef só é válido com o push concluído; antes disso a imagem não existe.
	if status != StatusSucceeded {
		st.ImageRef = ""
	} else if m := reDigest.FindStringSubmatch(logs); m != nil {
		st.Digest = m[1]
	}
	return st, nil
}

func (d *Docker) Delete(ctx context.Context, id string) error {
	// rm -f derruba mesmo se ainda estiver rodando. Não-existe → idempotente.
	if _, err := d.run(ctx, "rm", "-f", buildContainerName(id)); err != nil && !isNoSuchObject(err) {
		return err
	}
	return nil
}

// mapBuildState traduz o status do Docker + exit code num estado semântico de build.
// running/created/restarting → running; exited 0 → succeeded; qualquer outro → failed.
func mapBuildState(status string, exitCode int) string {
	switch status {
	case "running", "created", "restarting", "paused":
		return StatusRunning
	case "exited":
		if exitCode == 0 {
			return StatusSucceeded
		}
		return StatusFailed
	default:
		// dead/removing/desconhecido: tratamos como falha terminal.
		return StatusFailed
	}
}

// dockerInspect é o subconjunto da saída de `docker inspect` que o builder consome.
type dockerInspect struct {
	State struct {
		Status   string `json:"Status"`
		ExitCode int    `json:"ExitCode"`
	} `json:"State"`
	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
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

// run executa o binário do Docker com os args dados (sem shell) e captura
// stdout/stderr. Para `logs`, stdout+stderr juntos são a saída do build.
func (d *Docker) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, d.cfg.Bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String() + stderr.String(), fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String() + stderr.String(), nil
}

// --- helpers ---

func buildContainerName(id string) string { return "adila-" + id }
func label(key, value string) string      { return labelPrefix + key + "=" + value }

// isNoSuchObject reconhece o erro do Docker para objeto inexistente, para 404/idempotência.
func isNoSuchObject(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such container") ||
		strings.Contains(msg, "no such object")
}
