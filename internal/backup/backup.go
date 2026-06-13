package backup

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/adila/dash/worker/internal/runtime"
)

// R2Config agrupa as credenciais e configuração do bucket R2.
type R2Config struct {
	AccountID       string
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string
}

// Enabled reporta se todas as credenciais necessárias estão presentes.
func (c R2Config) Enabled() bool {
	return c.AccountID != "" && c.Bucket != "" && c.AccessKeyID != "" && c.SecretAccessKey != ""
}

// Runner executa backups periódicos de containers gerenciados para o R2.
// Só faz backup de containers running: container parado não aceita docker exec.
type Runner struct {
	dockerBin string
	r2        R2Config
	interval  time.Duration
	retention int // número de backups a manter por recurso
	log       *slog.Logger
}

func NewRunner(dockerBin string, r2 R2Config, interval time.Duration, retention int, log *slog.Logger) *Runner {
	if log == nil {
		log = slog.Default()
	}
	return &Runner{dockerBin: dockerBin, r2: r2, interval: interval, retention: retention, log: log}
}

// Run bloqueia até ctx ser cancelado, disparando um ciclo de backup a cada interval.
func (r *Runner) Run(ctx context.Context, rt runtime.ContainerRuntime) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.backupAll(ctx, rt)
		}
	}
}

func (r *Runner) backupAll(ctx context.Context, rt runtime.ContainerRuntime) {
	instances, err := rt.ListRunning(ctx)
	if err != nil {
		r.log.Error("backup: listar containers", "err", err)
		return
	}
	for _, inst := range instances {
		if err := r.backupInstance(ctx, inst); err != nil {
			r.log.Error("backup falhou", "id", inst.ID, "kind", string(inst.Kind), "err", err)
		}
	}
}

func (r *Runner) backupInstance(ctx context.Context, inst *runtime.Instance) error {
	switch inst.Kind {
	case runtime.KindPostgres:
		return r.backupPostgres(ctx, inst)
	case runtime.KindRedis:
		return r.backupRedis(ctx, inst)
	default:
		return nil
	}
}

// backupPostgres extrai um pg_dump custom-format via docker exec (sem shell),
// envia para R2 e prune backups antigos.
func (r *Runner) backupPostgres(ctx context.Context, inst *runtime.Instance) error {
	name := runtime.ContainerName(inst.ID)
	cmd := exec.CommandContext(ctx, r.dockerBin,
		"exec", name,
		"pg_dump", "-U", "app", "-d", "app",
		"--format=custom", "-Z1")
	data, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("pg_dump: %w", err)
	}
	key := backupKey(inst.ID, ".pgdump")
	if err := r.upload(ctx, key, data); err != nil {
		return err
	}
	r.log.Info("backup postgres OK", "id", inst.ID, "bytes", len(data))
	return r.pruneOld(ctx, inst.ID, ".pgdump")
}

// backupRedis emite SAVE para forçar o dump.rdb e lê o arquivo via docker exec.
func (r *Runner) backupRedis(ctx context.Context, inst *runtime.Instance) error {
	name := runtime.ContainerName(inst.ID)
	// SAVE é síncrono: bloqueia até o dump estar gravado no disco.
	saveCmd := exec.CommandContext(ctx, r.dockerBin, "exec", name,
		"redis-cli", "-a", inst.Password, "--no-auth-warning", "SAVE")
	if err := saveCmd.Run(); err != nil {
		return fmt.Errorf("redis SAVE: %w", err)
	}
	catCmd := exec.CommandContext(ctx, r.dockerBin, "exec", name,
		"cat", "/data/dump.rdb")
	data, err := catCmd.Output()
	if err != nil {
		return fmt.Errorf("ler dump.rdb: %w", err)
	}
	key := backupKey(inst.ID, ".rdb")
	if err := r.upload(ctx, key, data); err != nil {
		return err
	}
	r.log.Info("backup redis OK", "id", inst.ID, "bytes", len(data))
	return r.pruneOld(ctx, inst.ID, ".rdb")
}

// backupKey gera a chave R2 com timestamp lexicograficamente ordenável.
// Ordem lexicográfica == ordem cronológica para o formato 20060102T150405Z.
func backupKey(id, ext string) string {
	ts := time.Now().UTC().Format("20060102T150405Z")
	return "backups/" + id + "/" + ts + ext
}

func (r *Runner) r2URL(key string) string {
	return "https://" + r.r2.AccountID + ".r2.cloudflarestorage.com/" + r.r2.Bucket + "/" + key
}

func (r *Runner) upload(ctx context.Context, key string, data []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, r.r2URL(key), bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("upload criar request: %w", err)
	}
	req.ContentLength = int64(len(data))
	req.Header.Set("content-type", "application/octet-stream")
	signRequest(req, r.r2.AccessKeyID, r.r2.SecretAccessKey, data)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("upload R2 PUT: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("upload R2 status %d: %s", resp.StatusCode, body)
	}
	return nil
}

// pruneOld lista os backups do recurso e apaga os mais antigos além de retention.
func (r *Runner) pruneOld(ctx context.Context, id, ext string) error {
	prefix := "backups/" + id + "/"
	keys, err := r.listObjects(ctx, prefix)
	if err != nil {
		return fmt.Errorf("listar para prune: %w", err)
	}
	var matching []string
	for _, k := range keys {
		if strings.HasSuffix(k, ext) {
			matching = append(matching, k)
		}
	}
	sort.Strings(matching) // lexicográfico = cronológico
	if len(matching) <= r.retention {
		return nil
	}
	for _, k := range matching[:len(matching)-r.retention] {
		if err := r.deleteObject(ctx, k); err != nil {
			r.log.Error("prune: apagar objeto", "key", k, "err", err)
		}
	}
	return nil
}

// listBucketResult parseia o XML de ListBucketResult do S3/R2.
type listBucketResult struct {
	XMLName  xml.Name        `xml:"ListBucketResult"`
	Contents []bucketContent `xml:"Contents"`
}

type bucketContent struct {
	Key string `xml:"Key"`
}

func (r *Runner) listObjects(ctx context.Context, prefix string) ([]string, error) {
	baseURL := "https://" + r.r2.AccountID + ".r2.cloudflarestorage.com/" + r.r2.Bucket
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL, nil)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("prefix", prefix)
	req.URL.RawQuery = q.Encode()
	signRequest(req, r.r2.AccessKeyID, r.r2.SecretAccessKey, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("listar objetos R2: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("listar objetos R2 status %d: %s", resp.StatusCode, body)
	}
	var result listBucketResult
	if err := xml.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("parse ListBucketResult: %w", err)
	}
	keys := make([]string, 0, len(result.Contents))
	for _, item := range result.Contents {
		keys = append(keys, item.Key)
	}
	return keys, nil
}

func (r *Runner) deleteObject(ctx context.Context, key string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, r.r2URL(key), nil)
	if err != nil {
		return err
	}
	signRequest(req, r.r2.AccessKeyID, r.r2.SecretAccessKey, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("delete R2: %w", err)
	}
	defer resp.Body.Close()
	// R2 devolve 204 em delete com sucesso; alguns gateways retornam 200.
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("delete R2 status %d: %s", resp.StatusCode, body)
	}
	return nil
}
