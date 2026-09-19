package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	methodPluginRegister    = "plugin.register"
	methodPluginReconfigure = "plugin.reconfigure"
	methodSchedulerPick     = "scheduler.pick"
	methodHostAuthList      = "host.auth.list"
	pluginName              = "header-credential-router"
)

var (
	currentConfig atomic.Value
	authCache     credentialCache
)

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

type pluginMetadata struct {
	Name             string        `json:"Name"`
	Version          string        `json:"Version"`
	Author           string        `json:"Author"`
	GitHubRepository string        `json:"GitHubRepository"`
	ConfigFields     []configField `json:"ConfigFields,omitempty"`
}

type configField struct {
	Name        string   `json:"Name"`
	Type        string   `json:"Type"`
	EnumValues  []string `json:"EnumValues,omitempty"`
	Description string   `json:"Description"`
}

type registration struct {
	SchemaVersion uint32                   `json:"schema_version"`
	Metadata      pluginMetadata           `json:"metadata"`
	Capabilities  registrationCapabilities `json:"capabilities"`
}

type registrationCapabilities struct {
	Scheduler                 bool `json:"scheduler"`
	SchedulerAcrossPriorities bool `json:"scheduler_across_priorities,omitempty"`
}

type schedulerPickRequest struct {
	Provider   string                   `json:"Provider"`
	Providers  []string                 `json:"Providers"`
	Model      string                   `json:"Model"`
	Stream     bool                     `json:"Stream"`
	Options    schedulerOptions         `json:"Options"`
	Candidates []schedulerAuthCandidate `json:"Candidates"`
}

type schedulerOptions struct {
	Headers  map[string][]string `json:"Headers"`
	Metadata map[string]any      `json:"Metadata"`
}

type schedulerAuthCandidate struct {
	ID         string            `json:"ID"`
	Provider   string            `json:"Provider"`
	Priority   int               `json:"Priority"`
	Status     string            `json:"Status"`
	Attributes map[string]string `json:"Attributes"`
}

type schedulerPickResponse struct {
	AuthID          string `json:"AuthID"`
	DelegateBuiltin string `json:"DelegateBuiltin"`
	Handled         bool   `json:"Handled"`
}

type hostAuthListResponse struct {
	Files []hostAuthFileEntry `json:"files"`
}

type hostAuthFileEntry struct {
	ID        string `json:"id,omitempty"`
	AuthIndex string `json:"auth_index,omitempty"`
	Name      string `json:"name"`
	Provider  string `json:"provider,omitempty"`
	Label     string `json:"label,omitempty"`
	Status    string `json:"status,omitempty"`
	Disabled  bool   `json:"disabled,omitempty"`
	Account   string `json:"account,omitempty"`
	Email     string `json:"email,omitempty"`
}

type credentialCache struct {
	mu      sync.Mutex
	expires time.Time
	files   []hostAuthFileEntry
}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case methodPluginRegister, methodPluginReconfigure:
		if err := configure(request); err != nil {
			return nil, err
		}
		return okEnvelope(pluginRegistration())
	case methodSchedulerPick:
		return pickCredential(request)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method, http.StatusBadRequest), nil
	}
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: schemaVersion,
		Metadata: pluginMetadata{
			Name:             pluginName,
			Version:          "0.1.0",
			Author:           "dryangkun",
			GitHubRepository: "https://github.com/dryangkun/cliproxyapi-header-credential-plugin",
			ConfigFields: []configField{
				{Name: "header", Type: "string", Description: "Inbound header containing the credential selector."},
				{Name: "match_field", Type: "enum", EnumValues: []string{"account", "email", "label", "name", "auth_index", "id", "auto"}, Description: "Credential field matched against the header value."},
				{Name: "case_sensitive", Type: "boolean", Description: "Whether matching is case-sensitive."},
				{Name: "missing_behavior", Type: "enum", EnumValues: []string{"fallback", "reject"}, Description: "Action when the selector header is absent."},
				{Name: "not_found_behavior", Type: "enum", EnumValues: []string{"fallback", "reject"}, Description: "Action when no available candidate matches."},
				{Name: "cache_ttl_seconds", Type: "integer", Description: "Seconds to cache host credential metadata. Zero disables caching."},
			},
		},
		Capabilities: registrationCapabilities{
			Scheduler:                 true,
			SchedulerAcrossPriorities: true,
		},
	}
}

func pickCredential(raw []byte) ([]byte, error) {
	var req schedulerPickRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("decode scheduler request: %w", err)
	}

	cfg := loadedConfig()
	requested := strings.TrimSpace(headerValue(req.Options.Headers, cfg.Header))
	if requested == "" {
		if cfg.MissingBehavior == "reject" {
			return errorEnvelope("credential_header_required", fmt.Sprintf("request header %s is required", cfg.Header), http.StatusBadRequest), nil
		}
		return okEnvelope(schedulerPickResponse{Handled: false})
	}

	files, err := authCache.list(time.Duration(cfg.CacheTTLSeconds) * time.Second)
	if err != nil {
		return errorEnvelope("credential_lookup_failed", err.Error(), http.StatusServiceUnavailable), nil
	}

	authID, matches := matchCredential(requested, cfg, req.Candidates, files)
	switch len(matches) {
	case 1:
		return okEnvelope(schedulerPickResponse{AuthID: authID, Handled: true})
	case 0:
		if cfg.NotFoundBehavior == "fallback" {
			return okEnvelope(schedulerPickResponse{Handled: false})
		}
		return errorEnvelope(
			"credential_not_found",
			fmt.Sprintf("credential %q from header %s is not an available candidate for this request", requested, cfg.Header),
			http.StatusBadRequest,
		), nil
	default:
		return errorEnvelope(
			"credential_ambiguous",
			fmt.Sprintf("credential selector %q matched multiple available credentials: %s", requested, strings.Join(matches, ", ")),
			http.StatusBadRequest,
		), nil
	}
}

func matchCredential(requested string, cfg pluginConfig, candidates []schedulerAuthCandidate, files []hostAuthFileEntry) (string, []string) {
	candidateIDs := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if id := strings.TrimSpace(candidate.ID); id != "" {
			candidateIDs[id] = struct{}{}
		}
	}

	matches := make([]string, 0, 1)
	for _, entry := range files {
		id := strings.TrimSpace(entry.ID)
		if _, ok := candidateIDs[id]; !ok {
			continue
		}
		if credentialEntryMatches(requested, cfg, entry) {
			matches = append(matches, id)
		}
	}
	if len(matches) == 1 {
		return matches[0], matches
	}
	return "", matches
}

func credentialEntryMatches(requested string, cfg pluginConfig, entry hostAuthFileEntry) bool {
	for _, value := range credentialFieldValues(cfg.MatchField, entry) {
		if valuesEqual(requested, value, cfg.CaseSensitive) {
			return true
		}
	}
	return false
}

func credentialFieldValues(field string, entry hostAuthFileEntry) []string {
	switch field {
	case "account":
		return []string{entry.Account}
	case "email":
		return []string{entry.Email}
	case "label":
		return []string{entry.Label}
	case "name":
		return []string{entry.Name}
	case "auth_index":
		return []string{entry.AuthIndex}
	case "id":
		return []string{entry.ID}
	case "auto":
		return []string{entry.Account, entry.Email, entry.Label, entry.Name, entry.AuthIndex, entry.ID}
	default:
		return nil
	}
}

func valuesEqual(left, right string, caseSensitive bool) bool {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if left == "" || right == "" {
		return false
	}
	if caseSensitive {
		return left == right
	}
	return strings.EqualFold(left, right)
}

func headerValue(headers map[string][]string, name string) string {
	for key, values := range headers {
		if strings.EqualFold(strings.TrimSpace(key), name) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}

func (c *credentialCache) list(ttl time.Duration) ([]hostAuthFileEntry, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	if ttl > 0 && len(c.files) > 0 && now.Before(c.expires) {
		return append([]hostAuthFileEntry(nil), c.files...), nil
	}
	result, err := callHost(methodHostAuthList, map[string]any{})
	if err != nil {
		return nil, err
	}
	var resp hostAuthListResponse
	if err := json.Unmarshal(result, &resp); err != nil {
		return nil, fmt.Errorf("decode host.auth.list result: %w", err)
	}
	c.files = append(c.files[:0], resp.Files...)
	if ttl > 0 {
		c.expires = now.Add(ttl)
	} else {
		c.expires = time.Time{}
	}
	return append([]hostAuthFileEntry(nil), c.files...), nil
}

func (c *credentialCache) invalidate() {
	c.mu.Lock()
	c.files = nil
	c.expires = time.Time{}
	c.mu.Unlock()
}
