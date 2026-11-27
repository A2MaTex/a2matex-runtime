package config

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestProductionFileEnvironmentAndCaseSensitiveTrust(t *testing.T) {
	clearEnvironment(t)
	file := writeConfigFile(t, `production:
  authorization_enabled: true
  clock_skew: 12s
  verification_keys:
    Execution.Test: PUBLIC-FILE
`)
	cfg, err := Load(Overrides{ConfigFile: file})
	if err != nil || !cfg.Production.AuthorizationEnabled || cfg.Production.ClockSkew != 12*time.Second ||
		cfg.Production.VerificationKeys["Execution.Test"] != "PUBLIC-FILE" || len(cfg.Production.VerificationKeys) != 1 {
		t.Fatal("file lost trust identifier/cfg", err)
	}
	t.Setenv("A2MATEX_RUNTIME_PRODUCTION_CLOCK_SKEW", "0s")
	t.Setenv("A2MATEX_RUNTIME_PRODUCTION_VERIFICATION_KEYS", `{"ENV.Key":"PUBLIC-ENV"}`)
	cfg, err = Load(Overrides{ConfigFile: file})
	if err != nil || cfg.Production.ClockSkew != 0 || cfg.Production.VerificationKeys["ENV.Key"] != "PUBLIC-ENV" || len(cfg.Production.VerificationKeys) != 1 {
		t.Fatal("environment did not replace file map", err)
	}
	t.Setenv("A2MATEX_RUNTIME_PRODUCTION_AUTHORIZATION_ENABLED", "false")
	cfg, err = Load(Overrides{ConfigFile: file})
	if err != nil || cfg.Production.AuthorizationEnabled {
		t.Fatal("boolean environment precedence", err)
	}
	// JSON is also accepted, with exact kid spelling preserved.
	clearEnvironment(t)
	content, err := json.Marshal(map[string]any{"production": map[string]any{
		"authorization_enabled": true, "verification_keys": map[string]string{"JSON.Key": "PUBLIC"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(Overrides{ConfigFile: writeConfigFile(t, string(content))})
	if err != nil || cfg.Production.VerificationKeys["JSON.Key"] != "PUBLIC" {
		t.Fatal("JSON trust map", err)
	}
}

func TestProductionRejectsMalformedStructureWithoutPayloadLeaks(t *testing.T) {
	for _, content := range []string{
		`production: null`, `production: []`, `production: {unknown: {}}`, `production.authorization_enabled: true`,
		`production: {}
Production: {}`,
		`production: {authorization_enabled: "true"}`, `production: {authorization_enabled: null}`,
		`production: {authorization_enabled: true}`, `production: {authorization_enabled: true, verification_keys: {}}`,
		`production: {clock_skew: ""}`, `production: {clock_skew: 30}`, `production: {clock_skew: -1s}`, `production: {clock_skew: 31s}`,
		`production: {verification_keys: null}`, `production: {verification_keys: {key: 12}}`,
		`production: {verification_keys: "PRIVATE"}`, `production: {clock_skew: 1s, CLOCK_SKEW: 2s}`,
	} {
		t.Run(content, func(t *testing.T) {
			clearEnvironment(t)
			_, err := Load(Overrides{ConfigFile: writeConfigFile(t, content)})
			if err == nil || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatal("invalid config accepted or leaked", err)
			}
		})
	}
	for _, test := range []struct{ key, value string }{
		{"AUTHORIZATION_ENABLED", "1"}, {"CLOCK_SKEW", "PRIVATE"}, {"CLOCK_SKEW", "31s"},
		{"VERIFICATION_KEYS", `null`}, {"VERIFICATION_KEYS", `[]`}, {"VERIFICATION_KEYS", `{"key":null}`},
		{"VERIFICATION_KEYS", `{"key":1}`}, {"VERIFICATION_KEYS", `{"key":"PRIVATE","k\u0065y":"other"}`},
		{"VERIFICATION_KEYS", `{"key":"PRIVATE"} {}`},
	} {
		t.Run(test.key+test.value, func(t *testing.T) {
			clearEnvironment(t)
			t.Setenv("A2MATEX_RUNTIME_PRODUCTION_"+test.key, test.value)
			if _, err := Load(Overrides{}); err == nil || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatal("invalid env accepted or leaked", err)
			}
		})
	}
}
