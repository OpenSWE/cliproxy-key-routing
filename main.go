package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);

static const cliproxy_host_api* stored_host;

static void store_host_api(const cliproxy_host_api* host) { stored_host = host; }

static int call_host_api(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (stored_host == NULL || stored_host->call == NULL) {
		return 1;
	}
	return stored_host->call(stored_host->host_ctx, method, request, request_len, response);
}

static void free_host_buffer(void* ptr, size_t len) {
	if (stored_host != NULL && stored_host->free_buffer != NULL && ptr != NULL) {
		stored_host->free_buffer(ptr, len);
	}
}
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

const (
	pluginName    = "cliproxy-key-routing"
	pluginVersion = "0.3.0"
	defaultFile   = "host-routing.yaml"
)

type pluginConfig struct {
	// File is the sidecar path. Relative paths resolve against the plugin's own directory.
	File string `yaml:"file"`
}

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

type registration struct {
	SchemaVersion uint32             `json:"schema_version"`
	Metadata      pluginapi.Metadata `json:"metadata"`
	Capabilities  struct {
		Scheduler bool `json:"scheduler"`
	} `json:"capabilities"`
}

type hostLog struct {
	Level   string         `json:"level"`
	Message string         `json:"message"`
	Fields  map[string]any `json:"fields,omitempty"`
}

var current atomic.Pointer[table]

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	C.store_host_api(host)
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr, response.len = nil, 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required"))
		return 1
	}
	var raw []byte
	if request != nil && requestLen > 0 {
		raw = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	out, err := handle(C.GoString(method), raw)
	if err != nil {
		writeResponse(response, errorEnvelope("plugin_error", err.Error()))
		return 1
	}
	writeResponse(response, out)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, _ C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {}

func handle(method string, raw []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		if err := configure(raw); err != nil {
			return nil, err
		}
		return okEnvelope(register())
	case pluginabi.MethodSchedulerPick:
		return pick(raw)
	case pluginabi.MethodPluginQuiesce, pluginabi.MethodPluginShutdown:
		return okEnvelope(struct{}{})
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func configure(raw []byte) error {
	var req lifecycleRequest
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &req); err != nil {
			return err
		}
	}
	var cfg pluginConfig
	if len(req.ConfigYAML) > 0 {
		if err := yaml.Unmarshal(req.ConfigYAML, &cfg); err != nil {
			return err
		}
	}
	path := strings.TrimSpace(cfg.File)
	if path == "" {
		path = defaultFile
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(pluginDir(), path)
	}
	t := newTable(path)
	current.Store(t)
	if _, err := t.current(); err != nil {
		logHost("warn", "key-routing: routing file unavailable, all picks pass through", map[string]any{"file": path, "err": err.Error()})
	} else {
		logHost("info", "key-routing: routing file loaded", map[string]any{"file": path})
	}
	return nil
}

func register() registration {
	r := registration{SchemaVersion: pluginabi.SchemaVersion}
	r.Metadata = pluginapi.Metadata{
		Name:             pluginName,
		Version:          pluginVersion,
		Author:           "soulmachine",
		GitHubRepository: "https://github.com/soulmachine/cliproxy-key-routing",
		ConfigFields: []pluginapi.ConfigField{{
			Name:        "file",
			Type:        pluginapi.ConfigFieldTypeString,
			Description: "Routing YAML: inbound key -> ordered account emails. Relative to the plugins directory.",
		}},
	}
	r.Capabilities.Scheduler = true
	return r
}

func pick(raw []byte) ([]byte, error) {
	var req pluginapi.SchedulerPickRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	unhandled := func() ([]byte, error) { return okEnvelope(pluginapi.SchedulerPickResponse{Handled: false}) }

	t := current.Load()
	if t == nil {
		return unhandled()
	}
	r, err := t.current()
	if err != nil {
		logHost("warn", "key-routing: routing file error", map[string]any{"err": err.Error()})
	}
	if r == nil {
		return unhandled()
	}

	key := inboundKey(req.Options.Headers)
	d := decide(r, key, req.Candidates)
	fields := map[string]any{"key": keySuffix(key), "provider": req.Provider, "model": req.Model}
	if name := r.byKey[key].Name; name != "" {
		fields["host"] = name
	}
	switch {
	case d.Reject:
		fields["reason"] = d.Reason
		fields["candidates"] = len(req.Candidates)
		logHost("warn", "key-routing: reject", fields)
	case d.AuthID == "":
		fields["reason"] = d.Reason
		fields["candidates"] = len(req.Candidates)
		logHost("warn", "key-routing: pass-through", fields)
	default:
		fields["account"] = d.Account
		fields["rank"] = d.Rank
		fields["auth"] = d.AuthID
		logHost("info", "key-routing: pick", fields)
	}
	return okEnvelope(d.response())
}

// pluginDir is the directory the host loaded this dylib from.
func pluginDir() string {
	if p := dylibPath(); p != "" {
		return filepath.Dir(p)
	}
	return "."
}

// logFieldOrder is the display order for the fields folded into a log message.
var logFieldOrder = []string{"host", "key", "account", "rank", "auth", "reason", "candidates", "model", "provider", "file", "err"}

// logHost writes one line into the gateway log. The host's formatter prints only
// its own whitelist of structured fields (provider, model, error, ...) and drops
// the rest, so every field is folded into the message text instead.
func logHost(level, msg string, fields map[string]any) {
	var b strings.Builder
	b.WriteString(msg)
	seen := make(map[string]bool, len(fields))
	for _, k := range logFieldOrder {
		if v, ok := fields[k]; ok {
			fmt.Fprintf(&b, " %s=%v", k, v)
			seen[k] = true
		}
	}
	rest := make([]string, 0, len(fields))
	for k := range fields {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	for _, k := range rest {
		fmt.Fprintf(&b, " %s=%v", k, fields[k])
	}
	_ = callHost(pluginabi.MethodHostLog, hostLog{Level: level, Message: b.String()})
}

func callHost(method string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	var resp C.cliproxy_buffer
	var reqPtr *C.uint8_t
	if len(raw) > 0 {
		p := C.CBytes(raw)
		if p == nil {
			return nil
		}
		defer C.free(p)
		reqPtr = (*C.uint8_t)(p)
	}
	C.call_host_api(cMethod, reqPtr, C.size_t(len(raw)), &resp)
	if resp.ptr != nil {
		C.free_host_buffer(resp.ptr, resp.len)
	}
	return nil
}

func okEnvelope(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope{OK: true, Result: raw})
}

func errorEnvelope(code, msg string) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: msg}})
	return raw
}

func writeResponse(resp *C.cliproxy_buffer, raw []byte) {
	if resp == nil || len(raw) == 0 {
		return
	}
	p := C.CBytes(raw)
	if p == nil {
		return
	}
	resp.ptr, resp.len = p, C.size_t(len(raw))
}
