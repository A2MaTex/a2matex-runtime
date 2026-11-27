package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"

	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
)

const defaultProductionClockSkew = 30 * time.Second

type ProductionConfig struct {
	AuthorizationEnabled bool              `mapstructure:"authorization_enabled"`
	VerificationKeys     map[string]string `mapstructure:"verification_keys"`
	ClockSkew            time.Duration     `mapstructure:"clock_skew"`
}

func (c ProductionConfig) Validate() error {
	if c.ClockSkew < 0 || c.ClockSkew > constants.ExecutionLeaseMaxClockSkew {
		return productionInvalid("clock_skew")
	}
	if c.AuthorizationEnabled && len(c.VerificationKeys) == 0 {
		return productionInvalid("verification_keys")
	}
	return nil
}

// Viper folds nested map keys, including signing kid values. Read only this
// typed section before its normal strict decode, preserving exact trust IDs.
// YAML also accepts JSON; other existing config formats remain usable without
// this section and can supply production settings through the environment.
func loadProduction(loader *viper.Viper, file string) (ProductionConfig, error) {
	cfg := ProductionConfig{ClockSkew: defaultProductionClockSkew}
	fields := map[string]any{}
	if file != "" {
		ext := strings.ToLower(filepath.Ext(file))
		if ext != ".yaml" && ext != ".yml" && ext != ".json" {
			if loader.InConfig("production") {
				return cfg, productionInvalid("file_format")
			}
		} else {
			raw, err := os.ReadFile(file)
			if err != nil {
				return cfg, productionInvalid("file_unavailable")
			}
			var document map[string]any
			if err := yaml.Unmarshal(raw, &document); err != nil {
				return cfg, productionInvalid("section_invalid")
			}
			found := false
			for name, value := range document {
				if strings.HasPrefix(strings.ToLower(name), "production.") {
					return cfg, productionInvalid("section_invalid")
				}
				if strings.EqualFold(name, "production") {
					if found {
						return cfg, productionInvalid("field_duplicate")
					}
					found = true
					var ok bool
					fields, ok = value.(map[string]any)
					if !ok {
						return cfg, productionInvalid("section_invalid")
					}
				}
			}
		}
	}
	seen := make(map[string]bool, len(fields))
	for name, value := range fields {
		name = strings.ToLower(name)
		if seen[name] {
			return cfg, productionInvalid("field_duplicate")
		}
		seen[name] = true
		switch name {
		case "authorization_enabled":
			enabled, ok := value.(bool)
			if !ok {
				return cfg, productionInvalid(name)
			}
			cfg.AuthorizationEnabled = enabled
		case "clock_skew":
			text, ok := value.(string)
			duration, err := time.ParseDuration(text)
			if !ok || err != nil {
				return cfg, productionInvalid(name)
			}
			cfg.ClockSkew = duration
		case "verification_keys":
			values, ok := value.(map[string]any)
			if !ok {
				return cfg, productionInvalid(name)
			}
			cfg.VerificationKeys = make(map[string]string, len(values))
			for kid, raw := range values {
				key, ok := raw.(string)
				if !ok {
					return cfg, productionInvalid(name)
				}
				cfg.VerificationKeys[kid] = key
			}
		default:
			return cfg, productionInvalid("field_unknown")
		}
	}
	// Match existing Viper semantics: an empty environment value is unset.
	if value := os.Getenv(envPrefix + "_PRODUCTION_AUTHORIZATION_ENABLED"); value != "" {
		if value != "true" && value != "false" {
			return cfg, productionInvalid("authorization_enabled")
		}
		cfg.AuthorizationEnabled = value == "true"
	}
	if value := os.Getenv(envPrefix + "_PRODUCTION_CLOCK_SKEW"); value != "" {
		duration, err := time.ParseDuration(value)
		if err != nil {
			return cfg, productionInvalid("clock_skew")
		}
		cfg.ClockSkew = duration
	}
	if value := os.Getenv(envPrefix + "_PRODUCTION_VERIFICATION_KEYS"); value != "" {
		keys, err := decodeVerificationKeys(value)
		if err != nil {
			return cfg, err
		}
		cfg.VerificationKeys = keys
	}
	// Hide only the already validated section from Viper's lossy map conversion.
	// Config receives the original case-sensitive map after UnmarshalExact.
	loader.Set("production.authorization_enabled", cfg.AuthorizationEnabled)
	loader.Set("production.clock_skew", cfg.ClockSkew)
	loader.Set("production.verification_keys", map[string]string{})
	return cfg, cfg.Validate()
}

func decodeVerificationKeys(value string) (map[string]string, error) {
	if !utf8.ValidString(value) {
		return nil, productionInvalid("verification_keys")
	}
	decoder := json.NewDecoder(bytes.NewBufferString(value))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return nil, productionInvalid("verification_keys")
	}
	keys := make(map[string]string)
	for decoder.More() {
		token, err := decoder.Token()
		kid, ok := token.(string)
		if err != nil || !ok {
			return nil, productionInvalid("verification_keys")
		}
		if _, duplicate := keys[kid]; duplicate {
			return nil, productionInvalid("verification_keys")
		}
		token, err = decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return nil, productionInvalid("verification_keys")
		}
		keys[kid] = key
	}
	if _, err := decoder.Token(); err != nil {
		return nil, productionInvalid("verification_keys")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, productionInvalid("verification_keys")
	}
	return keys, nil
}

func productionInvalid(field string) error {
	return invalidSetting("production."+field, fmt.Errorf("production authorization configuration is invalid"))
}
