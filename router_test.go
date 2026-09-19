package main

import "testing"

func TestHeaderValueCaseInsensitive(t *testing.T) {
	headers := map[string][]string{"x-cpa-credential": {"alice"}}
	if got := headerValue(headers, "X-CPA-Credential"); got != "alice" {
		t.Fatalf("got %q", got)
	}
}

func TestMatchCredentialUsesEligibleCandidatesOnly(t *testing.T) {
	cfg := defaultConfig()
	candidates := []schedulerAuthCandidate{{ID: "auth-a"}, {ID: "auth-b"}}
	files := []hostAuthFileEntry{
		{ID: "auth-a", Account: "alice"},
		{ID: "auth-b", Account: "bob"},
		{ID: "auth-c", Account: "alice"},
	}

	id, matches := matchCredential("Alice", cfg, candidates, files)
	if id != "auth-a" || len(matches) != 1 {
		t.Fatalf("id=%q matches=%v", id, matches)
	}
}

func TestMatchCredentialAuto(t *testing.T) {
	cfg := defaultConfig()
	cfg.MatchField = "auto"
	candidates := []schedulerAuthCandidate{{ID: "auth-a"}}
	files := []hostAuthFileEntry{{
		ID: "auth-a", Account: "acct", Email: "alice@example.com", Label: "prod",
	}}
	id, matches := matchCredential("prod", cfg, candidates, files)
	if id != "auth-a" || len(matches) != 1 {
		t.Fatalf("id=%q matches=%v", id, matches)
	}
}

func TestMatchCredentialAmbiguous(t *testing.T) {
	cfg := defaultConfig()
	candidates := []schedulerAuthCandidate{{ID: "auth-a"}, {ID: "auth-b"}}
	files := []hostAuthFileEntry{
		{ID: "auth-a", Account: "shared"},
		{ID: "auth-b", Account: "shared"},
	}
	id, matches := matchCredential("shared", cfg, candidates, files)
	if id != "" || len(matches) != 2 {
		t.Fatalf("id=%q matches=%v", id, matches)
	}
}

func TestDecodeConfigYAML(t *testing.T) {
	cfg := defaultConfig()
	raw := []byte(`
header: X-Route-Credential
match_field: auto
case_sensitive: true
missing_behavior: reject
not_found_behavior: fallback
cache_ttl_seconds: 12
priority: 100 # host-owned and ignored
`)
	if err := decodeConfigYAML(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Header != "X-Route-Credential" || cfg.MatchField != "auto" ||
		!cfg.CaseSensitive || cfg.CacheTTLSeconds != 12 ||
		cfg.MissingBehavior != "reject" || cfg.NotFoundBehavior != "fallback" {
		t.Fatalf("cfg=%+v", cfg)
	}
}

func TestRegistrationRequestsCandidatesAcrossPriorities(t *testing.T) {
	reg := pluginRegistration()
	if !reg.Capabilities.Scheduler || !reg.Capabilities.SchedulerAcrossPriorities {
		t.Fatalf("capabilities=%+v", reg.Capabilities)
	}
}
