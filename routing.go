// Package main is a CLIProxyAPI scheduler plugin that orders upstream
// credentials per inbound API key.
//
// A sidecar YAML maps each inbound key to an ordered list of account emails.
// On every pick the plugin finds the first email in the caller's row that
// matches a live candidate's auth file name and returns that credential.
// Candidates are pre-filtered by the host for cooldowns and rate limits, and
// a failed credential is excluded on retry, so the order is a fallback chain.
package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

type route struct {
	Key   string   `yaml:"key"`
	Name  string   `yaml:"name,omitempty"`
	Order []string `yaml:"order"`
	// Strict turns the order into an allowlist: with no named account live the
	// request fails instead of falling back to the host's round-robin.
	Strict bool `yaml:"strict,omitempty"`
}

type routingFile struct {
	Routes []route `yaml:"routes"`
}

type routes struct {
	byKey map[string]route
}

// table caches the sidecar and re-reads it when its mtime changes.
type table struct {
	path string

	mu      sync.Mutex
	modTime time.Time
	size    int64
	loaded  *routes
	loadErr error
}

func newTable(path string) *table { return &table{path: path} }

// current returns the routing table, reloading it when the file changed.
// ponytail: one stat per pick; cache by (mtime,size), no fsnotify.
func (t *table) current() (*routes, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	info, err := os.Stat(t.path)
	if err != nil {
		return nil, err
	}
	if t.loaded != nil && info.ModTime().Equal(t.modTime) && info.Size() == t.size {
		return t.loaded, t.loadErr
	}
	raw, err := os.ReadFile(t.path)
	if err != nil {
		return nil, err
	}
	parsed, err := parseRoutes(raw)
	t.modTime, t.size = info.ModTime(), info.Size()
	if err != nil {
		// Keep serving the last good table; surface the error once per change.
		t.loadErr = err
		if t.loaded == nil {
			return nil, err
		}
		return t.loaded, err
	}
	t.loaded, t.loadErr = parsed, nil
	return parsed, nil
}

func parseRoutes(raw []byte) (*routes, error) {
	var f routingFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("parse routing file: %w", err)
	}
	out := &routes{byKey: make(map[string]route, len(f.Routes))}
	for i, r := range f.Routes {
		key := strings.TrimSpace(r.Key)
		if key == "" {
			return nil, fmt.Errorf("routes[%d]: empty key", i)
		}
		if len(r.Order) == 0 {
			return nil, fmt.Errorf("routes[%d] (%s): empty order", i, keySuffix(key))
		}
		if _, dup := out.byKey[key]; dup {
			return nil, fmt.Errorf("routes[%d]: duplicate key %s", i, keySuffix(key))
		}
		order := make([]string, 0, len(r.Order))
		for _, e := range r.Order {
			e = strings.ToLower(strings.TrimSpace(e))
			if e == "" {
				return nil, fmt.Errorf("routes[%d] (%s): empty order entry", i, keySuffix(key))
			}
			order = append(order, e)
		}
		out.byKey[key] = route{Key: key, Name: strings.TrimSpace(r.Name), Order: order, Strict: r.Strict}
	}
	return out, nil
}

// inboundKey extracts the caller's API key from the headers the host forwards.
func inboundKey(h map[string][]string) string {
	hdr := http.Header(h)
	if v := strings.TrimSpace(hdr.Get("Authorization")); v != "" {
		if len(v) > 7 && strings.EqualFold(v[:7], "Bearer ") {
			return strings.TrimSpace(v[7:])
		}
		return v
	}
	for _, name := range []string{"X-Api-Key", "X-Goog-Api-Key"} {
		if v := strings.TrimSpace(hdr.Get(name)); v != "" {
			return v
		}
	}
	return ""
}

// keySuffix is the only form of a key that ever reaches a log line.
func keySuffix(key string) string {
	if len(key) <= 4 {
		return "…" + key
	}
	return "…" + key[len(key)-4:]
}

type decision struct {
	AuthID  string // chosen candidate; empty means unhandled
	Account string // email that matched
	Rank    int    // 0-based position in the row
	Reason  string // for the log line when unhandled
	Reject  bool   // strict row with no named account live: fail the request
}

// response maps a decision to the host's pick response.
func (d decision) response() pluginapi.SchedulerPickResponse {
	switch {
	case d.Reject:
		return pluginapi.SchedulerPickResponse{Handled: true, Reject: true, RejectReason: "key-routing: " + d.Reason}
	case d.AuthID != "":
		return pluginapi.SchedulerPickResponse{AuthID: d.AuthID, Handled: true}
	}
	return pluginapi.SchedulerPickResponse{Handled: false}
}

var errNoKey = errors.New("no inbound api key")

// decide implements the ordering: first email in the row with a live candidate wins.
// Match is case-insensitive substring on the candidate ID (its auth file name).
func decide(r *routes, key string, candidates []pluginapi.SchedulerAuthCandidate) decision {
	if key == "" {
		return decision{Reason: "no inbound key"}
	}
	row, ok := r.byKey[key]
	if !ok {
		return decision{Reason: "unbound key"}
	}
	for rank, email := range row.Order {
		for _, c := range candidates {
			if strings.Contains(strings.ToLower(c.ID), email) {
				return decision{AuthID: c.ID, Account: email, Rank: rank}
			}
		}
	}
	if row.Strict {
		return decision{Reason: "strict row, no named account live", Reject: true}
	}
	return decision{Reason: "no named account live"}
}
