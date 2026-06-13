package runtime

// Estados semânticos que o agent reporta. São exatamente as chaves que o
// HetznerDatabaseProvider.mapStatus (lado TS) reconhece — manter em sincronia.
const (
	statusCreating  = "creating"
	statusStarting  = "starting"
	statusRunning   = "running"
	statusUnhealthy = "unhealthy"
	statusPaused    = "paused"
	statusStopped   = "stopped"
	statusCrashed   = "crashed"
	statusDeleting  = "deleting"
)

// mapDockerState traduz o estado do Docker (State.Status + ExitCode + Health.Status)
// para o status semântico do agent. É a peça que distingue parada intencional de crash:
//
//   - exited com código 0  → stopped  (shutdown limpo: suspend ou parada normal)
//   - exited com código !=0 → crashed (morreu com erro)
//   - running + health=starting   → starting  (container subiu, Postgres ainda não aceita conexão)
//   - running + health=unhealthy  → unhealthy (pg_isready falhando)
//   - running + healthy/sem-health → running
//
// Um status desconhecido cai em creating (transitório) — espelha o default conservador
// do lado TS, que não marca FAILED por engano numa versão nova do Docker/agent.
func mapDockerState(dockerStatus string, exitCode int, health string) string {
	switch dockerStatus {
	case "created":
		return statusCreating
	case "running":
		switch health {
		case "starting":
			return statusStarting
		case "unhealthy":
			return statusUnhealthy
		default: // "healthy" ou sem healthcheck configurado
			return statusRunning
		}
	case "paused":
		return statusPaused
	case "restarting":
		// on-failure ainda tentando subir: trata como transitório, não como crash final.
		return statusStarting
	case "removing":
		return statusDeleting
	case "exited":
		if exitCode == 0 {
			return statusStopped
		}
		return statusCrashed
	case "dead":
		return statusCrashed
	default:
		return statusCreating
	}
}
