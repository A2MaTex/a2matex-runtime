package config

import (
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/spf13/viper"
)

const (
	envPrefix = "A2MATEX_RUNTIME"

	defaultShutdownTimeout = 10 * time.Second
	minShutdownTimeout     = time.Second
	maxShutdownTimeout     = time.Minute

	defaultListenAddress     = "127.0.0.1:6810"
	defaultReadHeaderTimeout = 5 * time.Second
	defaultReadTimeout       = 10 * time.Second
	defaultWriteTimeout      = 30 * time.Second
	defaultIdleTimeout       = time.Minute

	minHTTPTimeout       = time.Second
	maxReadHeaderTimeout = 30 * time.Second
	maxReadTimeout       = 2 * time.Minute
	maxWriteTimeout      = 2 * time.Minute
	maxIdleTimeout       = 5 * time.Minute

	defaultLogLevel  = "info"
	defaultLogFormat = "json"
)

type Config struct {
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
	if err := loader.UnmarshalExact(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode configuration: %w", err)
	}
	cfg.normalize()

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func setDefaults(loader *viper.Viper) {
	loader.SetDefault("shutdown_timeout", defaultShutdownTimeout)
	loader.SetDefault("http.listen_address", defaultListenAddress)
	loader.SetDefault("http.read_header_timeout", defaultReadHeaderTimeout)
	loader.SetDefault("http.read_timeout", defaultReadTimeout)
	loader.SetDefault("http.write_timeout", defaultWriteTimeout)
	loader.SetDefault("http.idle_timeout", defaultIdleTimeout)
	loader.SetDefault("logging.level", defaultLogLevel)
	loader.SetDefault("logging.format", defaultLogFormat)
}

func configureEnvironment(loader *viper.Viper) {
	loader.SetEnvPrefix(envPrefix)
	loader.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	loader.AutomaticEnv()
}

func readConfigFile(loader *viper.Viper, configFile string) error {
	if configFile == "" {
		return nil
	}

	loader.SetConfigFile(configFile)
	if err := loader.ReadInConfig(); err != nil {
		return fmt.Errorf("read configuration file: %w", err)
	}

	return nil
}

func (c *Config) normalize() {
	c.Logging.Level = strings.ToLower(c.Logging.Level)
	c.Logging.Format = strings.ToLower(c.Logging.Format)
}

func (c Config) Validate() error {
	address, err := netip.ParseAddrPort(c.HTTP.ListenAddress)
	if err != nil {
		return fmt.Errorf("http.listen_address must be an IP address and port: %w", err)
	}
	if !address.Addr().IsLoopback() {
		return fmt.Errorf("http.listen_address must use a loopback IP, got %q", c.HTTP.ListenAddress)
	}
	if address.Port() == 0 {
		return fmt.Errorf("http.listen_address must use a non-zero port, got %q", c.HTTP.ListenAddress)
	}

	if err := validateDurationRange("shutdown_timeout", c.ShutdownTimeout, minShutdownTimeout, maxShutdownTimeout); err != nil {
		return err
	}
	if err := validateDurationRange("http.read_header_timeout", c.HTTP.ReadHeaderTimeout, minHTTPTimeout, maxReadHeaderTimeout); err != nil {
		return err
	}
	if err := validateDurationRange("http.read_timeout", c.HTTP.ReadTimeout, minHTTPTimeout, maxReadTimeout); err != nil {
		return err
	}
	if err := validateDurationRange("http.write_timeout", c.HTTP.WriteTimeout, minHTTPTimeout, maxWriteTimeout); err != nil {
		return err
	}
	if err := validateDurationRange("http.idle_timeout", c.HTTP.IdleTimeout, minHTTPTimeout, maxIdleTimeout); err != nil {
		return err
	}

	switch c.Logging.Level {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("logging.level must be debug, info, warn, or error, got %q", c.Logging.Level)
	}

	switch c.Logging.Format {
	case "json", "text":
	default:
		return fmt.Errorf("logging.format must be json or text, got %q", c.Logging.Format)
	}

	return nil
}

func validateDurationRange(name string, value, minimum, maximum time.Duration) error {
	if value < minimum || value > maximum {
		return fmt.Errorf("%s must be between %s and %s, got %s", name, minimum, maximum, value)
	}

	return nil
}
