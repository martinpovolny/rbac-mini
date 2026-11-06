package store

import "testing"

// TestRebind: Postgres uses $n placeholders while our portable SQL is
// written with ?. Rebind converts at the call site (SPEC §3); SQLite passes
// queries through unchanged.
func TestRebind(t *testing.T) {
	got := Rebind("postgres", "WHERE t.org_id = ? AND pr.username = ? AND p.application IN (?,?)")
	want := "WHERE t.org_id = $1 AND pr.username = $2 AND p.application IN ($3,$4)"
	if got != want {
		t.Errorf("Rebind = %q, want %q", got, want)
	}
	if got := Rebind("sqlite", "WHERE a = ?"); got != "WHERE a = ?" {
		t.Errorf("sqlite Rebind = %q, want unchanged", got)
	}
}
