package runtime

import "testing"

func TestPickFreePort(t *testing.T) {
	cases := []struct {
		name    string
		used    map[int]bool
		start   int
		end     int
		want    int
		wantErr bool
	}{
		{"range vazio devolve o start", map[int]bool{}, 20000, 20002, 20000, false},
		{"pula portas usadas", map[int]bool{20000: true, 20001: true}, 20000, 20002, 20002, false},
		{"range unitário livre", map[int]bool{}, 30000, 30000, 30000, false},
		{"range esgotado erra", map[int]bool{20000: true, 20001: true}, 20000, 20001, 0, true},
		{"ignora usadas fora do range", map[int]bool{19999: true, 30001: true}, 20000, 20000, 20000, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := pickFreePort(tc.used, tc.start, tc.end)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("quer erro, veio nil (porta %d)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("erro inesperado: %v", err)
			}
			if got != tc.want {
				t.Fatalf("porta = %d, quer %d", got, tc.want)
			}
		})
	}
}

func TestInstanceHostPortFromLabel(t *testing.T) {
	// Label específico do kind (pg.hostport ou redis.hostport).
	const pgLabelKey = "pg.hostport"
	const redisLabelKey = "redis.hostport"

	pgLbl := labelPrefix + pgLabelKey
	redisLbl := labelPrefix + redisLabelKey

	// Label tem prioridade mesmo sem bind ativo (container parado → Ports vazio).
	if p := instanceHostPortFromLabel(map[string]string{pgLbl: "20005"}, pgLabelKey, nil); p != 20005 {
		t.Fatalf("Postgres — label sem bind: p=%d, quer 20005", p)
	}
	if p := instanceHostPortFromLabel(map[string]string{redisLbl: "30100"}, redisLabelKey, nil); p != 30100 {
		t.Fatalf("Redis — label sem bind: p=%d, quer 30100", p)
	}

	// Sem label → cai pro bind ativo (container legado sem o label).
	binds := []portBinding{{HostIP: "127.0.0.1", HostPort: "32768"}}
	if p := instanceHostPortFromLabel(map[string]string{}, pgLabelKey, binds); p != 32768 {
		t.Fatalf("fallback bind: p=%d, quer 32768", p)
	}

	// Label inválido → fallback pro bind.
	if p := instanceHostPortFromLabel(map[string]string{pgLbl: "lixo"}, pgLabelKey, binds); p != 32768 {
		t.Fatalf("label inválido → fallback: p=%d, quer 32768", p)
	}

	// Sem label e sem bind → 0 (connection não é montada).
	if p := instanceHostPortFromLabel(map[string]string{}, pgLabelKey, nil); p != 0 {
		t.Fatalf("sem nada: p=%d, quer 0", p)
	}
}
