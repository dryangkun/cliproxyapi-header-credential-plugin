package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
)

const (
	methodPluginRegister    = "plugin.register"
	methodPluginReconfigure = "plugin.reconfigure"
	methodSchedulerPick     = "scheduler.pick"
	pluginName              = "header-credential-router"
)

var currentConfig atomic.Value

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
			Version:          "0.2.0",
			Author:           "dryangkun",
			GitHubRepository: "https://github.com/dryangkun/cliproxyapi-header-credential-plugin",
			ConfigFields: []configField{
				{Name: "header", Type: "string", Description: "Inbound header containing the exact CLIProxyAPI credential ID."},
				{Name: "missing_behavior", Type: "enum", EnumValues: []string{"fallback", "reject"}, Description: "Action when the credential ID header is absent."},
				{Name: "not_found_behavior", Type: "enum", EnumValues: []string{"fallback", "reject"}, Description: "Action when no available candidate has the requested credential ID."},
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

	authID, matches := matchCredentialID(requested, req.Candidates)
	switch matches {
	case 1:
		return okEnvelope(schedulerPickResponse{AuthID: authID, Handled: true})
	case 0:
		if cfg.NotFoundBehavior == "fallback" {
			return okEnvelope(schedulerPickResponse{Handled: false})
		}
		return errorEnvelope(
			"credential_not_found",
			fmt.Sprintf("credential id %q from header %s is not an available candidate for this request", requested, cfg.Header),
			http.StatusBadRequest,
		), nil
	default:
		return errorEnvelope(
			"credential_ambiguous",
			fmt.Sprintf("credential id %q matched multiple available candidates", requested),
			http.StatusBadRequest,
		), nil
	}
}

func matchCredentialID(requested string, candidates []schedulerAuthCandidate) (string, int) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return "", 0
	}

	matches := 0
	authID := ""
	for _, candidate := range candidates {
		id := strings.TrimSpace(candidate.ID)
		if id == requested {
			matches++
			authID = id
		}
	}
	if matches == 1 {
		return authID, matches
	}
	return "", matches
}

func headerValue(headers map[string][]string, name string) string {
	for key, values := range headers {
		if strings.EqualFold(strings.TrimSpace(key), name) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}
