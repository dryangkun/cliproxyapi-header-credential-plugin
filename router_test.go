package main

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func resetTestState() {
	routingState.reset()
}

func TestHeaderValuesCaseInsensitive(t *testing.T) {
	headers := map[string][]string{
		"x-cpa-credentials": {
			"codex-a@gmail.com-pro.json,codex-b@gmail.com-plus.json",
		},
	}
	values := headerValues(headers, "X-CPA-Credentials")
	if len(values) != 1 {
		t.Fatalf("values=%v", values)
	}
}

func TestParseCredentialIDs(t *testing.T) {
	got := parseCredentialIDs([]string{
		" a.json, b.json ",
		"b.json,c.json,,",
	})
	want := []string{"a.json", "b.json", "c.json"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%v want=%v", got, want)
	}
}

func TestFilterCredentialCandidates(t *testing.T) {
	candidates := []schedulerAuthCandidate{
		{ID: "a.json", Priority: 1},
		{ID: "b.json", Priority: 2},
		{ID: "c.json", Priority: 3},
	}
	got := filterCredentialCandidates([]string{"c.json", "a.json"}, candidates)
	if len(got) != 2 || got[0].ID != "a.json" || got[1].ID != "c.json" {
		t.Fatalf("got=%+v", got)
	}
}

func TestHighestPriorityCandidates(t *testing.T) {
	candidates := []schedulerAuthCandidate{
		{ID: "a.json", Priority: 10},
		{ID: "b.json", Priority: 20},
		{ID: "c.json", Priority: 20},
	}
	got := highestPriorityCandidates(candidates)
	if len(got) != 2 || got[0].ID != "b.json" || got[1].ID != "c.json" {
		t.Fatalf("got=%+v", got)
	}
}

func TestRoundRobinTracksLastID(t *testing.T) {
	resetTestState()
	candidates := []schedulerAuthCandidate{
		{ID: "a.json"},
		{ID: "b.json"},
		{ID: "c.json"},
	}

	first, err := routingState.pick("round-robin", "pool", candidates)
	if err != nil {
		t.Fatal(err)
	}
	second, err := routingState.pick("round-robin", "pool", candidates)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != "a.json" || second.ID != "b.json" {
		t.Fatalf("first=%s second=%s", first.ID, second.ID)
	}

	shrunk := []schedulerAuthCandidate{{ID: "a.json"}, {ID: "c.json"}}
	third, err := routingState.pick("round-robin", "pool", shrunk)
	if err != nil {
		t.Fatal(err)
	}
	if third.ID != "c.json" {
		t.Fatalf("third=%s", third.ID)
	}
}

func TestFillFirst(t *testing.T) {
	resetTestState()
	candidates := []schedulerAuthCandidate{{ID: "a.json"}, {ID: "b.json"}}
	got, err := routingState.pick("fill-first", "pool", candidates)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "a.json" {
		t.Fatalf("got=%s", got.ID)
	}
}

func TestWeightedRoundRobin(t *testing.T) {
	resetTestState()
	candidates := []schedulerAuthCandidate{
		{ID: "a.json", Attributes: map[string]string{"weight": "3"}},
		{ID: "b.json", Attributes: map[string]string{"weight": "1"}},
	}

	var got []string
	for i := 0; i < 4; i++ {
		picked, err := routingState.pick("weighted-round-robin", "pool", candidates)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, picked.ID)
	}
	want := []string{"a.json", "a.json", "b.json", "a.json"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%v want=%v", got, want)
	}
}

func TestSessionAffinityKeepsAvailableBinding(t *testing.T) {
	resetTestState()
	candidates := []schedulerAuthCandidate{
		{ID: "a.json", Priority: 10},
		{ID: "b.json", Priority: 10},
	}
	routeKey := "codex::gpt::pool"

	selected, err := routingState.pick("round-robin", routeKey, highestPriorityCandidates(candidates))
	if err != nil {
		t.Fatal(err)
	}
	routingState.bindSession(routeKey, "session-1", selected.ID, time.Hour)

	// Advance round robin. The session lookup must still return the original auth.
	if _, err := routingState.pick("round-robin", routeKey, candidates); err != nil {
		t.Fatal(err)
	}
	got, status := routingState.lookupSession(routeKey, "session-1", candidates, time.Hour)
	if got != selected.ID || status != "hit" {
		t.Fatalf("got=%s status=%s want=%s", got, status, selected.ID)
	}
}

func TestSessionAffinityCanKeepLowerPriorityBinding(t *testing.T) {
	resetTestState()
	routeKey := "codex::gpt::pool"
	routingState.bindSession(routeKey, "session-1", "low.json", time.Hour)

	candidates := []schedulerAuthCandidate{
		{ID: "high.json", Priority: 100},
		{ID: "low.json", Priority: 10},
	}
	got, status := routingState.lookupSession(routeKey, "session-1", candidates, time.Hour)
	if got != "low.json" || status != "hit" {
		t.Fatalf("got=%s status=%s", got, status)
	}
}

func TestSessionAffinityDropsUnavailableBinding(t *testing.T) {
	resetTestState()
	routeKey := "codex::gpt::pool"
	routingState.bindSession(routeKey, "session-1", "a.json", time.Hour)

	candidates := []schedulerAuthCandidate{{ID: "b.json", Priority: 10}}
	if got, status := routingState.lookupSession(routeKey, "session-1", candidates, time.Hour); got != "" || status != "unavailable" {
		t.Fatalf("unexpected binding=%s status=%s", got, status)
	}
}

func TestExplicitSessionID(t *testing.T) {
	headers := map[string][]string{
		"session-id": {"abc-123"},
	}
	if got := explicitSessionID(headers); got != "session-id:abc-123" {
		t.Fatalf("got=%q", got)
	}
}

func TestDecodeConfigYAML(t *testing.T) {
	cfg := defaultConfig()
	raw := []byte(`
header: X-Route-Credentials
strategy: weighted-round-robin
session_affinity: true
session_affinity_ttl: 30m
log_level: debug
missing_behavior: reject
not_found_behavior: fallback
priority: 100 # host-owned and ignored
`)
	if err := decodeConfigYAML(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Header != "X-Route-Credentials" ||
		cfg.Strategy != "weighted-round-robin" ||
		!cfg.SessionAffinity ||
		cfg.SessionAffinityTTL != 30*time.Minute ||
		cfg.LogLevel != "debug" ||
		cfg.MissingBehavior != "reject" ||
		cfg.NotFoundBehavior != "fallback" {
		t.Fatalf("cfg=%+v", cfg)
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := defaultConfig()
	if cfg.Header != "X-CPA-Credentials" ||
		cfg.Strategy != "round-robin" ||
		!cfg.SessionAffinity ||
		cfg.SessionAffinityTTL != time.Hour ||
		cfg.LogLevel != "info" {
		t.Fatalf("cfg=%+v", cfg)
	}
}

func TestRegistrationRequestsCandidatesAcrossPriorities(t *testing.T) {
	reg := pluginRegistration()
	if !reg.Capabilities.Scheduler || !reg.Capabilities.SchedulerAcrossPriorities || !reg.Capabilities.RequestInterceptor {
		t.Fatalf("capabilities=%+v", reg.Capabilities)
	}
}

func TestInterceptAfterAuthClearsRoutingHeader(t *testing.T) {
	currentConfig.Store(defaultConfig())
	raw := []byte(`{"RequestID":"req-1","Headers":{"X-CPA-Credentials":["a.json,b.json"],"Authorization":["Bearer x"]}}`)
	respRaw, err := interceptRequestAfterAuth(raw)
	if err != nil {
		t.Fatal(err)
	}

	var env envelope
	if err := json.Unmarshal(respRaw, &env); err != nil {
		t.Fatal(err)
	}
	if !env.OK {
		t.Fatalf("envelope error: %+v", env.Error)
	}

	var resp requestInterceptResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.ClearHeaders) != 1 || resp.ClearHeaders[0] != "X-CPA-Credentials" {
		t.Fatalf("clear headers=%v", resp.ClearHeaders)
	}
}
