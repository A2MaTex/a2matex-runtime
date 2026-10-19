package config

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/spf13/viper"
)

const (
	defaultConfigName = "config"
	defaultConfigPath = "configs"
	envPrefix         = "A2MATEX_RUNTIME"
)

type Config struct {
	Mode            string        `mapstructure:"mode"`
	ShutdownTimeout time.Duration `mapstructure:"shutdown_timeout"`
	HTTP            HTTPConfig    `mapstructure:"http"`
	Logging         LoggingConfig `mapstructure:"logging"`
}

type HTTPConfig struct {
	ListenAddress     string        `mapstructure:"listen_address"`
	ReadHeaderTimeout time.Duration `mapstructure:"read_header_timeout"`
	ReadTimeout       time.Duration `mapstructure:"read_timeout"`
	WriteTimeout      time.Duration `mapstructure:"write_timeout"`
	IdleTimeout       time.Duration `mapstructure:"idle_timeout"`
}

type LoggingConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
}

type Overrides struct {
	ConfigFile    string
	ListenAddress string
	LogLevel      string
}

func Load(overrides Overrides) (Config, error) {
	loader := viper.New()
	setDefaults(loader)
	configureEnvironment(loader)

	if err := readConfigFile(loader, overrides.ConfigFile); err != nil {
		return Config{}, err
	}

	if overrides.ListenAddress != "" {
		loader.Set("http.listen_address", overrides.ListenAddress)
	}
	if overrides.LogLevel != "" {
		loader.Set("logging.level", overrides.LogLevel)
	}

	var cfg Config
	if err := loader.Unmarshal(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode configuration: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func setDefaults(loader *viper.Viper) {
	loader.SetDefault("mode", "development")
	loader.SetDefault("shutdown_timeout", 10*time.Second)
	loader.SetDefault("http.listen_address", "127.0.0.1:8765")
	loader.SetDefault("http.read_header_timeout", 5*time.Second)
	loader.SetDefault("http.read_timeout", 10*time.Second)
	loader.SetDefault("http.write_timeout", 30*time.Second)
	loader.SetDefault("http.idle_timeout", 60*time.Second)
	loader.SetDefault("logging.level", "info")
	loader.SetDefault("logging.format", "json")
}

func configureEnvironment(loader *viper.Viper) {
	loader.SetEnvPrefix(envPrefix)
	loader.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	loader.AutomaticEnv()
}

func readConfigFile(loader *viper.Viper, configFile string) error {
	if configFile != "" {
		loader.SetConfigFile(configFile)
	} else {
		loader.SetConfigName(defaultConfigName)
		loader.SetConfigType("yaml")
		loader.AddConfigPath(defaultConfigPath)
	}

	if err := loader.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if configFile == "" && errors.As(err, &notFound) {
			return nil
		}

		return fmt.Errorf("read configuration file: %w", err)
	}

	return nil
}

func (c Config) Validate() error {
	if c.Mode != "development" && c.Mode != "production" {
		return fmt.Errorf("mode must be development or production, got %q", c.Mode)
	}

	address, err := netip.ParseAddrPort(c.HTTP.ListenAddress)
	if err != nil {
		return fmt.Errorf("http.listen_address must be an IP address and port: %w", err)
	}
	if !address.Addr().IsLoopback() {
		return fmt.Errorf("http.listen_address must use a loopback IP, got %q", c.HTTP.ListenAddress)
	}

	if err := validatePositiveDuration("shutdown_timeout", c.ShutdownTimeout); err != nil {
		return err
	}
	if err := validatePositiveDuration("http.read_header_timeout", c.HTTP.ReadHeaderTimeout); err != nil {
		return err
	}
	if err := validatePositiveDuration("http.read_timeout", c.HTTP.ReadTimeout); err != nil {
		return err
	}
	if err := validatePositiveDuration("http.write_timeout", c.HTTP.WriteTimeout); err != nil {
		return err
	}
	if err := validatePositiveDuration("http.idle_timeout", c.HTTP.IdleTimeout); err != nil {
		return err
	}

	switch strings.ToLower(c.Logging.Level) {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("logging.level must be debug, info, warn, or error, got %q", c.Logging.Level)
	}

	switch strings.ToLower(c.Logging.Format) {
	case "json", "text":
	default:
		return fmt.Errorf("logging.format must be json or text, got %q", c.Logging.Format)
	}

	return nil
}

func validatePositiveDuration(name string, value time.Duration) error {
	if value <= 0 {
		return fmt.Errorf("%s must be greater than zero", name)
	}

	return nil
}
