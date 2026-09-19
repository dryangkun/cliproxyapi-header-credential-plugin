package main

import "testing"

func TestHeaderValueCaseInsensitive(t *testing.T) {
	headers := map[string][]string{"x-cpa-credential": {"codex-user@gmail.com-pro.json"}}
	if got := headerValue(headers, "X-CPA-Credential"); got != "codex-user@gmail.com-pro.json" {
		t.Fatalf("got %q", got)
	}
}

func TestMatchCredentialID(t *testing.T) {
	candidates := []schedulerAuthCandidate{
		{ID: "codex-user@gmail.com-pro.json"},
		{ID: "codex-user@gmail.com-plus.json"},
	}

	id, matches := matchCredentialID("codex-user@gmail.com-plus.json", candidates)
	if id != "codex-user@gmail.com-plus.json" || matches != 1 {
		t.Fatalf("id=%q matches=%d", id, matches)
	}
}

func TestMatchCredentialIDIsExact(t *testing.T) {
	candidates := []schedulerAuthCandidate{
		{ID: "codex-user@gmail.com-pro.json"},
	}

	if id, matches := matchCredentialID("user@gmail.com", candidates); id != "" || matches != 0 {
		t.Fatalf("partial value unexpectedly matched: id=%q matches=%d", id, matches)
	}
	if id, matches := matchCredentialID("CODEX-USER@GMAIL.COM-PRO.JSON", candidates); id != "" || matches != 0 {
		t.Fatalf("case-insensitive value unexpectedly matched: id=%q matches=%d", id, matches)
	}
}

func TestMatchCredentialIDOnlyUsesCandidates(t *testing.T) {
	candidates := []schedulerAuthCandidate{
		{ID: "codex-a@gmail.com-pro.json"},
	}

	id, matches := matchCredentialID("codex-b@gmail.com-pro.json", candidates)
	if id != "" || matches != 0 {
		t.Fatalf("id=%q matches=%d", id, matches)
	}
}

func TestDecodeConfigYAML(t *testing.T) {
	cfg := defaultConfig()
	raw := []byte(`
header: X-Route-Credential
missing_behavior: reject
not_found_behavior: fallback
priority: 100 # host-owned and ignored
`)
	if err := decodeConfigYAML(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Header != "X-Route-Credential" ||
		cfg.MissingBehavior != "reject" ||
		cfg.NotFoundBehavior != "fallback" {
		t.Fatalf("cfg=%+v", cfg)
	}
}

func TestRegistrationRequestsCandidatesAcrossPriorities(t *testing.T) {
	reg := pluginRegistration()
	if !reg.Capabilities.Scheduler || !reg.Capabilities.SchedulerAcrossPriorities {
		t.Fatalf("capabilities=%+v", reg.Capabilities)
	}
}
