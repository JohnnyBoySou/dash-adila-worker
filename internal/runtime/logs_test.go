package runtime

import (
	"testing"
	"time"
)

func TestParseLogLinesSeparatesTimestamp(t *testing.T) {
	out := "2026-01-01T00:00:01.000000000Z servidor iniciado\n" +
		"2026-01-01T00:00:02.000000000Z escutando na porta 8080\n"
	lines := parseLogLines(out)
	if len(lines) != 2 {
		t.Fatalf("len = %d, quer 2", len(lines))
	}
	if lines[0].Message != "servidor iniciado" {
		t.Fatalf("msg[0] = %q", lines[0].Message)
	}
	want := time.Date(2026, 1, 1, 0, 0, 1, 0, time.UTC)
	if !lines[0].Timestamp.Equal(want) {
		t.Fatalf("ts[0] = %v, quer %v", lines[0].Timestamp, want)
	}
}

func TestParseLogLinesSortsByTimestamp(t *testing.T) {
	// Ordem de chegada invertida (stdout/stderr interleaving) deve ser corrigida.
	out := "2026-01-01T00:00:03.000000000Z terceira\n" +
		"2026-01-01T00:00:01.000000000Z primeira\n" +
		"2026-01-01T00:00:02.000000000Z segunda\n"
	lines := parseLogLines(out)
	want := []string{"primeira", "segunda", "terceira"}
	for i, l := range lines {
		if l.Message != want[i] {
			t.Fatalf("linha %d = %q, quer %q", i, l.Message, want[i])
		}
	}
}

func TestParseLogLinesEmpty(t *testing.T) {
	if lines := parseLogLines(""); lines != nil {
		t.Fatalf("saída vazia deveria devolver nil, veio %v", lines)
	}
	if lines := parseLogLines("\n\n"); len(lines) != 0 {
		t.Fatalf("só newlines deveria devolver vazio, veio %d", len(lines))
	}
}

func TestSplitLogTimestampFallback(t *testing.T) {
	// Linha sem carimbo parseável: timestamp zero, mensagem inteira preservada.
	ts, msg := splitLogTimestamp("linha sem carimbo")
	if !ts.IsZero() {
		t.Fatalf("ts = %v, quer zero", ts)
	}
	if msg != "linha sem carimbo" {
		t.Fatalf("msg = %q", msg)
	}
}
