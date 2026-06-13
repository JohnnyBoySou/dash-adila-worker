package runtime

import "testing"

func TestMapDockerState(t *testing.T) {
	cases := []struct {
		name        string
		dockerState string
		exitCode    int
		health      string
		want        string
	}{
		{"created vira creating", "created", 0, "", statusCreating},
		{"running sem health vira running", "running", 0, "", statusRunning},
		{"running healthy vira running", "running", 0, "healthy", statusRunning},
		{"running starting vira starting", "running", 0, "starting", statusStarting},
		{"running unhealthy vira unhealthy", "running", 0, "unhealthy", statusUnhealthy},
		{"paused vira paused", "paused", 0, "", statusPaused},
		{"restarting vira starting", "restarting", 0, "", statusStarting},
		{"removing vira deleting", "removing", 0, "", statusDeleting},
		{"exited 0 vira stopped", "exited", 0, "", statusStopped},
		{"exited !=0 vira crashed", "exited", 1, "", statusCrashed},
		{"exited 137 vira crashed", "exited", 137, "", statusCrashed},
		{"dead vira crashed", "dead", 0, "", statusCrashed},
		{"desconhecido vira creating", "frobnicated", 0, "", statusCreating},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mapDockerState(tc.dockerState, tc.exitCode, tc.health)
			if got != tc.want {
				t.Fatalf("mapDockerState(%q,%d,%q) = %q, quer %q",
					tc.dockerState, tc.exitCode, tc.health, got, tc.want)
			}
		})
	}
}
