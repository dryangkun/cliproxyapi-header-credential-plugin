package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type pluginConfig struct {
	Header           string
	MatchField       string
	CaseSensitive    bool
	MissingBehavior  string
	NotFoundBehavior string
	CacheTTLSeconds  int
}

func defaultConfig() pluginConfig {
	return pluginConfig{
		Header:           "X-CPA-Credential",
		MatchField:       "account",
		CaseSensitive:    false,
		MissingBehavior:  "fallback",
		NotFoundBehavior: "reject",
		CacheTTLSeconds:  5,
	}
}

func configure(raw []byte) error {
	var req lifecycleRequest
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &req); err != nil {
			return fmt.Errorf("decode lifecycle request: %w", err)
		}
	}

	cfg := defaultConfig()
	if len(req.ConfigYAML) > 0 {
		if err := decodeConfigYAML(req.ConfigYAML, &cfg); err != nil {
			return fmt.Errorf("decode plugin config: %w", err)
		}
	}
	if err := normalizeAndValidateConfig(&cfg); err != nil {
		return err
	}
	currentConfig.Store(cfg)
	authCache.invalidate()
	return nil
}

func loadedConfig() pluginConfig {
	if raw := currentConfig.Load(); raw != nil {
		if cfg, ok := raw.(pluginConfig); ok {
			return cfg
		}
	}
	return defaultConfig()
}

func normalizeAndValidateConfig(cfg *pluginConfig) error {
	cfg.Header = strings.TrimSpace(cfg.Header)
	cfg.MatchField = strings.ToLower(strings.TrimSpace(cfg.MatchField))
	cfg.MissingBehavior = strings.ToLower(strings.TrimSpace(cfg.MissingBehavior))
	cfg.NotFoundBehavior = strings.ToLower(strings.TrimSpace(cfg.NotFoundBehavior))

	if cfg.Header == "" {
		return fmt.Errorf("header must not be empty")
	}
	switch cfg.MatchField {
	case "account", "email", "label", "name", "auth_index", "id", "auto":
	default:
		return fmt.Errorf("invalid match_field")
	}
	switch cfg.MissingBehavior {
	case "fallback", "reject":
	default:
		return fmt.Errorf("missing_behavior must be fallback or reject")
	}
	switch cfg.NotFoundBehavior {
	case "fallback", "reject":
	default:
		return fmt.Errorf("not_found_behavior must be fallback or reject")
	}
	if cfg.CacheTTLSeconds < 0 || cfg.CacheTTLSeconds > 3600 {
		return fmt.Errorf("cache_ttl_seconds must be between 0 and 3600")
	}
	return nil
}

// CLIProxyAPI passes plugin-owned config as flat YAML. Keeping this parser small
// avoids coupling the plugin to a third-party YAML library.
func decodeConfigYAML(raw []byte, cfg *pluginConfig) error {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		line := strings.TrimSpace(stripYAMLComment(scanner.Text()))
		if line == "" || line == "---" || line == "..." {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		value = strings.Trim(value, "\"'")
		switch key {
		case "header":
			cfg.Header = value
		case "match_field":
			cfg.MatchField = value
		case "case_sensitive":
			v, err := strconv.ParseBool(strings.ToLower(value))
			if err != nil {
				return fmt.Errorf("case_sensitive must be true or false")
			}
			cfg.CaseSensitive = v
		case "missing_behavior":
			cfg.MissingBehavior = value
		case "not_found_behavior":
			cfg.NotFoundBehavior = value
		case "cache_ttl_seconds":
			v, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("cache_ttl_seconds must be an integer")
			}
			cfg.CacheTTLSeconds = v
		}
	}
	return scanner.Err()
}

func stripYAMLComment(line string) string {
	var quote rune
	for i, r := range line {
		switch r {
		case '\'', '"':
			if quote == 0 {
				quote = r
			} else if quote == r {
				quote = 0
			}
		case '#':
			if quote == 0 {
				return line[:i]
			}
		}
	}
	return line
}
