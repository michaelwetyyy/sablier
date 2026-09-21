package demand

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/viper"
)

type Config struct {
	PollInterval   time.Duration  `mapstructure:"poll_interval"`
	RequestTimeout time.Duration  `mapstructure:"request_timeout"`
	Metrics        MetricsConfig  `mapstructure:"metrics"`
	Sablier        SablierConfig  `mapstructure:"sablier"`
	Sources        []SourceConfig `mapstructure:"sources"`
}

type MetricsConfig struct {
	Listen string `mapstructure:"listen"`
}

type SablierConfig struct {
	URL string `mapstructure:"url"`
}

type SourceConfig struct {
	Name          string              `mapstructure:"name"`
	Type          string              `mapstructure:"type"`
	IdleAfter     time.Duration       `mapstructure:"idle_after"`
	FailurePolicy string              `mapstructure:"failure_policy"`
	Target        TargetConfig        `mapstructure:"target"`
	Parhelion     *ParhelionConfig    `mapstructure:"parhelion"`
	GiteaActions  *GiteaActionsConfig `mapstructure:"gitea_actions"`
}

type TargetConfig struct {
	Group string   `mapstructure:"group"`
	Names []string `mapstructure:"names"`
}

type ParhelionConfig struct {
	URL       string `mapstructure:"url"`
	TokenFile string `mapstructure:"token_file"`
	RunnerID  string `mapstructure:"runner_id"`
	OS        string `mapstructure:"os"`
}

type GiteaActionsConfig struct {
	URL       string `mapstructure:"url"`
	TokenFile string `mapstructure:"token_file"`
	Owner     string `mapstructure:"owner"`
	Repo      string `mapstructure:"repo"`
}

const (
	SourceTypeParhelion    = "parhelion"
	SourceTypeGiteaActions = "gitea-actions"
	FailurePolicyAwake     = "awake"
	FailurePolicyIgnore    = "ignore"
	FailurePolicyLastKnown = "last-known"
)

func LoadConfig(path string) (Config, error) {
	if strings.TrimSpace(path) == "" {
		return Config{}, errors.New("demand config path is required")
	}
	v := viper.New()
	v.SetConfigFile(path)
	if err := v.ReadInConfig(); err != nil {
		return Config{}, fmt.Errorf("read demand config: %w", err)
	}
	var conf Config
	if err := v.Unmarshal(&conf); err != nil {
		return Config{}, fmt.Errorf("decode demand config: %w", err)
	}
	applyDefaults(&conf)
	if err := conf.Validate(); err != nil {
		return Config{}, err
	}
	return conf, nil
}

func applyDefaults(conf *Config) {
	if conf.PollInterval == 0 {
		conf.PollInterval = 10 * time.Second
	}
	if conf.RequestTimeout == 0 {
		conf.RequestTimeout = 5 * time.Second
	}
	for i := range conf.Sources {
		if conf.Sources[i].FailurePolicy == "" {
			conf.Sources[i].FailurePolicy = FailurePolicyAwake
		}
	}
}

func (c Config) Validate() error {
	if c.PollInterval < time.Second {
		return fmt.Errorf("poll_interval must be at least 1s")
	}
	if c.RequestTimeout <= 0 {
		return fmt.Errorf("request_timeout must be positive")
	}
	if err := validateHTTPURL(c.Sablier.URL, "sablier.url"); err != nil {
		return err
	}
	if len(c.Sources) == 0 {
		return errors.New("at least one demand source is required")
	}

	seen := make(map[string]struct{}, len(c.Sources))
	for i, source := range c.Sources {
		prefix := fmt.Sprintf("sources[%d]", i)
		if source.Name == "" {
			return fmt.Errorf("%s.name is required", prefix)
		}
		if _, ok := seen[source.Name]; ok {
			return fmt.Errorf("duplicate demand source name %q", source.Name)
		}
		seen[source.Name] = struct{}{}
		if source.IdleAfter < 2*c.PollInterval {
			return fmt.Errorf("%s.idle_after must be at least twice poll_interval", prefix)
		}
		if source.FailurePolicy != FailurePolicyAwake && source.FailurePolicy != FailurePolicyIgnore && source.FailurePolicy != FailurePolicyLastKnown {
			return fmt.Errorf("%s.failure_policy must be %q, %q or %q", prefix, FailurePolicyAwake, FailurePolicyIgnore, FailurePolicyLastKnown)
		}
		if err := source.Target.validate(prefix + ".target"); err != nil {
			return err
		}
		switch source.Type {
		case SourceTypeParhelion:
			if source.Parhelion == nil || source.GiteaActions != nil {
				return fmt.Errorf("%s must define only parhelion configuration", prefix)
			}
			if err := validateHTTPURL(source.Parhelion.URL, prefix+".parhelion.url"); err != nil {
				return err
			}
		case SourceTypeGiteaActions:
			if source.GiteaActions == nil || source.Parhelion != nil {
				return fmt.Errorf("%s must define only gitea_actions configuration", prefix)
			}
			if err := validateHTTPURL(source.GiteaActions.URL, prefix+".gitea_actions.url"); err != nil {
				return err
			}
			if source.GiteaActions.Owner == "" || source.GiteaActions.Repo == "" {
				return fmt.Errorf("%s.gitea_actions.owner and repo are required", prefix)
			}
		default:
			return fmt.Errorf("%s.type %q is unsupported", prefix, source.Type)
		}
	}
	return nil
}

func (t TargetConfig) validate(prefix string) error {
	hasGroup := strings.TrimSpace(t.Group) != ""
	hasNames := len(t.Names) > 0
	if hasGroup == hasNames {
		return fmt.Errorf("%s must set exactly one of group or names", prefix)
	}
	for _, name := range t.Names {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("%s.names must not contain empty values", prefix)
		}
	}
	return nil
}

func validateHTTPURL(raw, field string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("%s must be an absolute http(s) URL", field)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("%s must use http or https", field)
	}
	return nil
}
