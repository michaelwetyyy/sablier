package demand

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"gotest.tools/v3/assert"
)

func writeDemandConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "demand.yaml")
	assert.NilError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func TestLoadConfig(t *testing.T) {
	t.Parallel()
	path := writeDemandConfig(t, `
poll_interval: 10s
request_timeout: 3s
metrics:
  listen: :10001
sablier:
  url: http://sablier:10000
sources:
  - name: parhelion
    type: parhelion
    idle_after: 30m
    target:
      group: parhelion
    parhelion:
      url: http://parhelion-controller:8000
      token_file: /secrets/parhelion
      runner_id: windows-01
      os: windows
  - name: gitops
    type: gitea-actions
    idle_after: 10m
    failure_policy: ignore
    target:
      names: [deployment_gitea-actions_runner_1]
    gitea_actions:
      url: http://gitea:3000
      token_file: /secrets/gitea
      owner: michael
      repo: homelab-gitops
`)
	conf, err := LoadConfig(path)
	assert.NilError(t, err)
	assert.Equal(t, conf.PollInterval, 10*time.Second)
	assert.Equal(t, conf.RequestTimeout, 3*time.Second)
	assert.Equal(t, conf.Metrics.Listen, ":10001")
	assert.Equal(t, conf.Sources[0].FailurePolicy, FailurePolicyAwake)
	assert.Equal(t, conf.Sources[0].IdleAfter, 30*time.Minute)
	assert.Equal(t, conf.Sources[1].FailurePolicy, FailurePolicyIgnore)
	assert.DeepEqual(t, conf.Sources[1].Target.Names, []string{"deployment_gitea-actions_runner_1"})
}

func TestLoadConfigDefaults(t *testing.T) {
	t.Parallel()
	path := writeDemandConfig(t, `
sablier:
  url: http://sablier:10000
sources:
  - name: gitops
    type: gitea-actions
    idle_after: 1m
    target: {group: gitops}
    gitea_actions:
      url: http://gitea:3000
      owner: michael
      repo: homelab-gitops
`)
	conf, err := LoadConfig(path)
	assert.NilError(t, err)
	assert.Equal(t, conf.PollInterval, 10*time.Second)
	assert.Equal(t, conf.RequestTimeout, 5*time.Second)
	assert.Equal(t, conf.Sources[0].FailurePolicy, FailurePolicyAwake)
}

func TestConfigValidation(t *testing.T) {
	t.Parallel()
	base := Config{
		PollInterval:   10 * time.Second,
		RequestTimeout: 5 * time.Second,
		Sablier:        SablierConfig{URL: "http://sablier:10000"},
		Sources: []SourceConfig{{
			Name: "x", Type: SourceTypeGiteaActions, IdleAfter: time.Minute, FailurePolicy: FailurePolicyAwake,
			Target:       TargetConfig{Group: "x"},
			GiteaActions: &GiteaActionsConfig{URL: "http://gitea:3000", Owner: "michael", Repo: "repo"},
		}},
	}

	t.Run("last-known failure policy accepted", func(t *testing.T) {
		conf := base
		conf.Sources = append([]SourceConfig(nil), base.Sources...)
		conf.Sources[0].FailurePolicy = FailurePolicyLastKnown
		assert.NilError(t, conf.Validate())
	})

	t.Run("idle duration protects against poll jitter", func(t *testing.T) {
		conf := base
		conf.Sources = append([]SourceConfig(nil), base.Sources...)
		conf.Sources[0].IdleAfter = 15 * time.Second
		assert.ErrorContains(t, conf.Validate(), "at least twice poll_interval")
	})

	t.Run("target group and names are exclusive", func(t *testing.T) {
		conf := base
		conf.Sources = append([]SourceConfig(nil), base.Sources...)
		conf.Sources[0].Target = TargetConfig{Group: "x", Names: []string{"y"}}
		assert.ErrorContains(t, conf.Validate(), "exactly one")
	})

	t.Run("duplicate names rejected", func(t *testing.T) {
		conf := base
		conf.Sources = append([]SourceConfig(nil), base.Sources...)
		conf.Sources = append(conf.Sources, conf.Sources[0])
		assert.ErrorContains(t, conf.Validate(), "duplicate")
	})
}
