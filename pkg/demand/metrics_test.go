package demand

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"gotest.tools/v3/assert"
)

func TestDemandMetricsExposeConfiguredTargets(t *testing.T) {
	t.Parallel()
	conf := Config{Sources: []SourceConfig{
		{
			Name: "gitops", Type: SourceTypeGiteaActions, IdleAfter: 10 * time.Minute,
			FailurePolicy: FailurePolicyAwake,
			Target:        TargetConfig{Names: []string{"deployment_ci_runner_1"}},
		},
		{
			Name: "parhelion", Type: SourceTypeParhelion, IdleAfter: 30 * time.Minute,
			FailurePolicy: FailurePolicyIgnore,
			Target:        TargetConfig{Group: "parhelion"},
		},
	}}
	m := NewMetrics(conf)
	assert.Equal(t, testutil.ToFloat64(m.targetInfo.WithLabelValues("gitops", SourceTypeGiteaActions, "name", "deployment_ci_runner_1", FailurePolicyAwake)), 1.0)
	assert.Equal(t, testutil.ToFloat64(m.targetIdleAfter.WithLabelValues("gitops", SourceTypeGiteaActions, "name", "deployment_ci_runner_1")), 600.0)
	assert.Equal(t, testutil.ToFloat64(m.targetInfo.WithLabelValues("parhelion", SourceTypeParhelion, "group", "parhelion", FailurePolicyIgnore)), 1.0)

	m.ObserveSource(conf.Sources[0], true, nil)
	assert.Equal(t, testutil.ToFloat64(m.sourceActive.WithLabelValues("gitops", SourceTypeGiteaActions)), 1.0)
	assert.Equal(t, testutil.ToFloat64(m.sourceCheckOK.WithLabelValues("gitops", SourceTypeGiteaActions)), 1.0)

	m.ObserveSource(conf.Sources[1], false, errors.New("source down"))
	assert.Equal(t, testutil.ToFloat64(m.sourceActive.WithLabelValues("parhelion", SourceTypeParhelion)), 0.0)
	assert.Equal(t, testutil.ToFloat64(m.sourceCheckOK.WithLabelValues("parhelion", SourceTypeParhelion)), 0.0)

	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, req)
	assert.Equal(t, rec.Code, 200)
	body := rec.Body.String()
	assert.Assert(t, strings.Contains(body, "sablier_demand_target_info"))
	assert.Assert(t, strings.Contains(body, `target="deployment_ci_runner_1"`))
	assert.Assert(t, strings.Contains(body, "sablier_demand_source_active"))
}
