package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	methodPluginRegister    = "plugin.register"
	methodPluginReconfigure = "plugin.reconfigure"
	methodSchedulerPick     = "scheduler.pick"
	pluginName              = "header-credential-router"
)

var (
	currentConfig atomic.Value
	routingState  pluginRoutingState
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

type sessionBinding struct {
	AuthID    string
	ExpiresAt time.Time
}

type smoothWeightedState struct {
	Current map[string]int64
	Weights map[string]int64
}

type pluginRoutingState struct {
	mu sync.Mutex

	LastPicked map[string]string
	Weighted   map[string]*smoothWeightedState
	Sessions   map[string]sessionBinding

	LastSessionCleanup time.Time
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
			Version:          "0.3.0",
			Author:           "dryangkun",
			GitHubRepository: "https://github.com/dryangkun/cliproxyapi-header-credential-plugin",
			ConfigFields: []configField{
				{Name: "header", Type: "string", Description: "Inbound header containing comma-separated CLIProxyAPI credential IDs."},
				{Name: "strategy", Type: "enum", EnumValues: []string{"round-robin", "weighted-round-robin", "fill-first"}, Description: "Routing strategy used inside the requested credential pool."},
				{Name: "session_affinity", Type: "boolean", Description: "Keep explicit client sessions pinned to the same credential while it remains available."},
				{Name: "session_affinity_ttl", Type: "string", Description: "Session binding lifetime, for example 1h or 30m."},
				{Name: "missing_behavior", Type: "enum", EnumValues: []string{"fallback", "reject"}, Description: "Action when the credential pool header is absent."},
				{Name: "not_found_behavior", Type: "enum", EnumValues: []string{"fallback", "reject"}, Description: "Action when none of the requested credential IDs are currently available."},
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
	requestedIDs := parseCredentialIDs(headerValues(req.Options.Headers, cfg.Header))
	if len(requestedIDs) == 0 {
		if cfg.MissingBehavior == "reject" {
			return errorEnvelope("credential_header_required", fmt.Sprintf("request header %s is required", cfg.Header), http.StatusBadRequest), nil
		}
		return okEnvelope(schedulerPickResponse{Handled: false})
	}

	candidates := filterCredentialCandidates(requestedIDs, req.Candidates)
	if len(candidates) == 0 {
		if cfg.NotFoundBehavior == "fallback" {
			return okEnvelope(schedulerPickResponse{Handled: false})
		}
		return errorEnvelope(
			"credential_not_found",
			fmt.Sprintf("none of the credential ids from header %s are available for this request", cfg.Header),
			http.StatusBadRequest,
		), nil
	}

	group := credentialGroupScope(requestedIDs)
	routeKey := routingScope(req, group)

	if cfg.SessionAffinity {
		if sessionID := explicitSessionID(req.Options.Headers); sessionID != "" {
			if authID := routingState.lookupSession(routeKey, sessionID, candidates, cfg.SessionAffinityTTL); authID != "" {
				return okEnvelope(schedulerPickResponse{AuthID: authID, Handled: true})
			}

			selected, err := routingState.pick(cfg.Strategy, routeKey, highestPriorityCandidates(candidates))
			if err != nil {
				return nil, err
			}
			routingState.bindSession(routeKey, sessionID, selected.ID, cfg.SessionAffinityTTL)
			return okEnvelope(schedulerPickResponse{AuthID: selected.ID, Handled: true})
		}
	}

	selected, err := routingState.pick(cfg.Strategy, routeKey, highestPriorityCandidates(candidates))
	if err != nil {
		return nil, err
	}
	return okEnvelope(schedulerPickResponse{AuthID: selected.ID, Handled: true})
}

func parseCredentialIDs(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{})
	out := make([]string, 0)
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			id := strings.TrimSpace(part)
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	return out
}

func filterCredentialCandidates(requestedIDs []string, candidates []schedulerAuthCandidate) []schedulerAuthCandidate {
	allowed := make(map[string]struct{}, len(requestedIDs))
	for _, id := range requestedIDs {
		allowed[id] = struct{}{}
	}

	seen := make(map[string]struct{})
	out := make([]schedulerAuthCandidate, 0, len(requestedIDs))
	for _, candidate := range candidates {
		id := strings.TrimSpace(candidate.ID)
		if _, ok := allowed[id]; !ok {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		candidate.ID = id
		seen[id] = struct{}{}
		out = append(out, candidate)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func highestPriorityCandidates(candidates []schedulerAuthCandidate) []schedulerAuthCandidate {
	if len(candidates) <= 1 {
		return candidates
	}
	best := candidates[0].Priority
	for i := 1; i < len(candidates); i++ {
		if candidates[i].Priority > best {
			best = candidates[i].Priority
		}
	}
	out := make([]schedulerAuthCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Priority == best {
			out = append(out, candidate)
		}
	}
	return out
}

func credentialGroupScope(ids []string) string {
	copyIDs := append([]string(nil), ids...)
	sort.Strings(copyIDs)
	return strings.Join(copyIDs, "\x1f")
}

func routingScope(req schedulerPickRequest, group string) string {
	provider := strings.ToLower(strings.TrimSpace(req.Provider))
	if provider == "" {
		providers := append([]string(nil), req.Providers...)
		for i := range providers {
			providers[i] = strings.ToLower(strings.TrimSpace(providers[i]))
		}
		sort.Strings(providers)
		provider = strings.Join(providers, ",")
	}
	return provider + "::" + strings.TrimSpace(req.Model) + "::" + group
}

func explicitSessionID(headers map[string][]string) string {
	for _, name := range []string{
		"X-Claude-Code-Session-Id",
		"Session-Id",
		"Session_id",
		"X-Session-ID",
		"X-Session-Affinity",
		"X-Client-Request-Id",
	} {
		if value := normalizeSessionID(headerValue(headers, name)); value != "" {
			return strings.ToLower(name) + ":" + value
		}
	}
	return ""
}

func normalizeSessionID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 2048 {
		return ""
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}
	return value
}

func (s *pluginRoutingState) pick(strategy, key string, candidates []schedulerAuthCandidate) (schedulerAuthCandidate, error) {
	if len(candidates) == 0 {
		return schedulerAuthCandidate{}, fmt.Errorf("no available credentials in requested pool")
	}

	switch strategy {
	case "fill-first":
		return candidates[0], nil
	case "round-robin":
		return s.pickRoundRobin(key, candidates), nil
	case "weighted-round-robin":
		return s.pickWeightedRoundRobin(key, candidates)
	default:
		return schedulerAuthCandidate{}, fmt.Errorf("unsupported strategy %q", strategy)
	}
}

func (s *pluginRoutingState) pickRoundRobin(key string, candidates []schedulerAuthCandidate) schedulerAuthCandidate {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.LastPicked == nil {
		s.LastPicked = make(map[string]string)
	}
	lastID := s.LastPicked[key]
	index := successorCandidateIndex(candidates, lastID)
	selected := candidates[index]
	s.LastPicked[key] = selected.ID
	return selected
}

func successorCandidateIndex(candidates []schedulerAuthCandidate, lastID string) int {
	if len(candidates) == 0 || lastID == "" {
		return 0
	}
	index := sort.Search(len(candidates), func(i int) bool {
		return candidates[i].ID > lastID
	})
	if index >= len(candidates) {
		return 0
	}
	return index
}

func (s *pluginRoutingState) pickWeightedRoundRobin(key string, candidates []schedulerAuthCandidate) (schedulerAuthCandidate, error) {
	weighted := make([]schedulerAuthCandidate, 0, len(candidates))
	weights := make(map[string]int64, len(candidates))
	for _, candidate := range candidates {
		weight := candidateWeight(candidate)
		if weight <= 0 {
			continue
		}
		weighted = append(weighted, candidate)
		weights[candidate.ID] = weight
	}
	if len(weighted) == 0 {
		return schedulerAuthCandidate{}, fmt.Errorf("no available credentials with positive weight in requested pool")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.Weighted == nil {
		s.Weighted = make(map[string]*smoothWeightedState)
	}
	state := s.Weighted[key]
	if state == nil {
		state = &smoothWeightedState{
			Current: make(map[string]int64),
			Weights: make(map[string]int64),
		}
		s.Weighted[key] = state
	}
	if weightsChanged(state.Weights, weights) {
		state.Current = make(map[string]int64)
		state.Weights = make(map[string]int64)
	}
	for id, weight := range weights {
		state.Weights[id] = weight
	}

	var selected schedulerAuthCandidate
	var selectedCurrent int64
	var totalWeight int64
	hasSelected := false
	for _, candidate := range weighted {
		weight := weights[candidate.ID]
		state.Current[candidate.ID] = saturatingAdd(state.Current[candidate.ID], weight)
		totalWeight = saturatingAdd(totalWeight, weight)
		if !hasSelected || state.Current[candidate.ID] > selectedCurrent {
			selected = candidate
			selectedCurrent = state.Current[candidate.ID]
			hasSelected = true
		}
	}
	state.Current[selected.ID] = saturatingAdd(state.Current[selected.ID], -totalWeight)
	return selected, nil
}

func candidateWeight(candidate schedulerAuthCandidate) int64 {
	raw := strings.TrimSpace(candidate.Attributes["weight"])
	if raw == "" {
		return 1
	}
	weight, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0
	}
	return weight
}

func weightsChanged(left, right map[string]int64) bool {
	if len(left) == 0 {
		return false
	}
	for id, weight := range right {
		if previous, ok := left[id]; ok && previous != weight {
			return true
		}
	}
	return false
}

func saturatingAdd(value, delta int64) int64 {
	if delta > 0 && value > math.MaxInt64-delta {
		return math.MaxInt64
	}
	if delta < 0 && value < math.MinInt64-delta {
		return math.MinInt64
	}
	return value + delta
}

func (s *pluginRoutingState) lookupSession(routeKey, sessionID string, candidates []schedulerAuthCandidate, ttl time.Duration) string {
	now := time.Now()
	cacheKey := routeKey + "::session::" + sessionID

	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupSessionsLocked(now, ttl)

	if s.Sessions == nil {
		return ""
	}
	binding, ok := s.Sessions[cacheKey]
	if !ok || !binding.ExpiresAt.After(now) {
		delete(s.Sessions, cacheKey)
		return ""
	}
	for _, candidate := range candidates {
		if candidate.ID == binding.AuthID {
			binding.ExpiresAt = now.Add(ttl)
			s.Sessions[cacheKey] = binding
			return binding.AuthID
		}
	}
	delete(s.Sessions, cacheKey)
	return ""
}

func (s *pluginRoutingState) bindSession(routeKey, sessionID, authID string, ttl time.Duration) {
	if sessionID == "" || authID == "" {
		return
	}
	now := time.Now()
	cacheKey := routeKey + "::session::" + sessionID

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Sessions == nil {
		s.Sessions = make(map[string]sessionBinding)
	}
	s.cleanupSessionsLocked(now, ttl)
	s.Sessions[cacheKey] = sessionBinding{
		AuthID:    authID,
		ExpiresAt: now.Add(ttl),
	}
}

func (s *pluginRoutingState) cleanupSessionsLocked(now time.Time, ttl time.Duration) {
	if !s.LastSessionCleanup.IsZero() && now.Sub(s.LastSessionCleanup) < minDuration(ttl/4, 5*time.Minute) {
		return
	}
	for key, binding := range s.Sessions {
		if !binding.ExpiresAt.After(now) {
			delete(s.Sessions, key)
		}
	}
	s.LastSessionCleanup = now
}

func minDuration(a, b time.Duration) time.Duration {
	if a <= 0 {
		return b
	}
	if a < b {
		return a
	}
	return b
}

func headerValues(headers map[string][]string, name string) []string {
	var out []string
	for key, values := range headers {
		if strings.EqualFold(strings.TrimSpace(key), name) {
			out = append(out, values...)
		}
	}
	return out
}

func headerValue(headers map[string][]string, name string) string {
	values := headerValues(headers, name)
	if len(values) == 0 {
		return ""
	}
	return values[0]
}


func (s *pluginRoutingState) reset() {
	s.mu.Lock()
	s.LastPicked = nil
	s.Weighted = nil
	s.Sessions = nil
	s.LastSessionCleanup = time.Time{}
	s.mu.Unlock()
}
