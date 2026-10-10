package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const sample = `
routes:
  - key: sk-personal
    name: host-a
    order: [personal@example.com, work@example.com, Ada.Lovelace@example.org]
  - key: sk-nickel
    order: [ada.lovelace@example.org, personal@example.com, work@example.com]
`

func cands(ids ...string) []pluginapi.SchedulerAuthCandidate {
	out := make([]pluginapi.SchedulerAuthCandidate, 0, len(ids))
	for _, id := range ids {
		out = append(out, pluginapi.SchedulerAuthCandidate{ID: id, Provider: "claude"})
	}
	return out
}

func TestDecide(t *testing.T) {
	r, err := parseRoutes([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	all := cands("claude-Ada.Lovelace@example.org.json", "claude-work@example.com.json", "claude-personal@example.com.json")

	if d := decide(r, "sk-personal", all); d.AuthID != "claude-personal@example.com.json" || d.Rank != 0 {
		t.Fatalf("personal primary: %+v", d)
	}
	// Primary cooling: host drops it from candidates; second in order wins.
	if d := decide(r, "sk-personal", cands("claude-Ada.Lovelace@example.org.json", "claude-work@example.com.json")); d.AuthID != "claude-work@example.com.json" || d.Rank != 1 {
		t.Fatalf("personal fallback: %+v", d)
	}
	// Case-insensitive match on the hashed filename format too.
	if d := decide(r, "sk-nickel", cands("claude-1a2b3c4d-ada.lovelace@example.org.json", "claude-personal@example.com.json")); d.AuthID != "claude-1a2b3c4d-ada.lovelace@example.org.json" {
		t.Fatalf("nickel hashed: %+v", d)
	}
	if d := decide(r, "sk-unknown", all); d.AuthID != "" || d.Reason != "unbound key" {
		t.Fatalf("unbound: %+v", d)
	}
	if d := decide(r, "", all); d.AuthID != "" {
		t.Fatalf("no key: %+v", d)
	}
	// Preference, not allowlist: nothing named is live -> unhandled, host round-robins the rest.
	if d := decide(r, "sk-personal", cands("codex-other@example.com.json")); d.AuthID != "" || d.Reason != "no named account live" {
		t.Fatalf("preference: %+v", d)
	}
}

func TestDecideStrict(t *testing.T) {
	r, err := parseRoutes([]byte("routes:\n  - key: sk-co\n    order: [work@example.com]\n    strict: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	d := decide(r, "sk-co", cands("claude-personal@example.com.json", "claude-work@example.com.json"))
	if d.AuthID != "claude-work@example.com.json" || d.Reject {
		t.Fatalf("strict pick: %+v", d)
	}
	if resp := d.response(); !resp.Handled || resp.Reject || resp.AuthID != d.AuthID {
		t.Fatalf("strict pick response: %+v", resp)
	}
	// Allowlist: the named account is cooling, so the request fails rather than use another account.
	d = decide(r, "sk-co", cands("claude-personal@example.com.json", "codex-personal@example.com.json"))
	if !d.Reject || d.AuthID != "" {
		t.Fatalf("strict reject: %+v", d)
	}
	if resp := d.response(); !resp.Handled || !resp.Reject || resp.AuthID != "" {
		t.Fatalf("strict reject response: %+v", resp)
	}
	// A non-strict row keeps passing through.
	r, _ = parseRoutes([]byte(sample))
	if resp := decide(r, "sk-personal", cands("codex-other@example.com.json")).response(); resp.Handled {
		t.Fatalf("preference row must stay unhandled: %+v", resp)
	}
}

func TestInboundKey(t *testing.T) {
	if k := inboundKey(map[string][]string{"Authorization": {"Bearer sk-abc"}}); k != "sk-abc" {
		t.Fatal(k)
	}
	if k := inboundKey(map[string][]string{"X-Api-Key": {"sk-xyz"}}); k != "sk-xyz" {
		t.Fatal(k)
	}
	if keySuffix("sk-1234567890") != "…7890" {
		t.Fatal(keySuffix("sk-1234567890"))
	}
}

func TestParseErrors(t *testing.T) {
	for _, bad := range []string{
		"routes:\n  - key: ''\n    order: [a]\n",
		"routes:\n  - key: k\n    order: []\n",
		"routes:\n  - key: k\n    order: [a]\n  - key: k\n    order: [b]\n",
	} {
		if _, err := parseRoutes([]byte(bad)); err == nil {
			t.Fatalf("expected error for %q", bad)
		}
	}
}

func TestTableReload(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "r.yaml")
	if err := os.WriteFile(p, []byte(sample), 0o600); err != nil {
		t.Fatal(err)
	}
	tb := newTable(p)
	r1, err := tb.current()
	if err != nil || len(r1.byKey) != 2 {
		t.Fatalf("first load: %v %+v", err, r1)
	}
	// Broken edit keeps the last good table and reports the error.
	os.Chtimes(p, time.Now(), time.Now().Add(2*time.Second))
	if err := os.WriteFile(p, []byte("routes: [ {key: k, order: []} ]"), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(p, time.Now(), time.Now().Add(4*time.Second))
	r2, err := tb.current()
	if err == nil || r2 == nil || len(r2.byKey) != 2 {
		t.Fatalf("bad edit should keep old table and error: %v %+v", err, r2)
	}
	// Good edit replaces it.
	if err := os.WriteFile(p, []byte("routes:\n  - key: only\n    order: [x@y]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(p, time.Now(), time.Now().Add(6*time.Second))
	r3, err := tb.current()
	if err != nil || len(r3.byKey) != 1 {
		t.Fatalf("reload: %v %+v", err, r3)
	}
}
