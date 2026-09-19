package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

type pluginConfig struct {
	Header           string
	MissingBehavior  string
	NotFoundBehavior string
}

func defaultConfig() pluginConfig {
	return pluginConfig{
		Header:           "X-CPA-Credential",
		MissingBehavior:  "fallback",
		NotFoundBehavior: "reject",
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
	cfg.MissingBehavior = strings.ToLower(strings.TrimSpace(cfg.MissingBehavior))
	cfg.NotFoundBehavior = strings.ToLower(strings.TrimSpace(cfg.NotFoundBehavior))

	if cfg.Header == "" {
		return fmt.Errorf("header must not be empty")
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
	return nil
}

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
		value = strings.Trim(strings.TrimSpace(value), "\"'")
		switch key {
		case "header":
			cfg.Header = value
		case "missing_behavior":
			cfg.MissingBehavior = value
		case "not_found_behavior":
			cfg.NotFoundBehavior = value
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
