package demand

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gotest.tools/v3/assert"
)

func TestGiteaActionsSource(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		body   string
		active bool
	}{
		{name: "queued", body: `{"workflow_runs":[{"status":"queued"}]}`, active: true},
		{name: "running", body: `{"workflow_runs":[{"status":"running"}]}`, active: true},
		{name: "completed", body: `{"workflow_runs":[{"status":"completed"}]}`, active: false},
		{name: "empty", body: `{"workflow_runs":[]}`, active: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, r.URL.Path, "/api/v1/repos/michael/homelab-gitops/actions/runs")
				assert.Equal(t, r.URL.Query().Get("limit"), "50")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			source := NewGiteaActionsSource(&http.Client{Timeout: time.Second}, GiteaActionsConfig{URL: server.URL, Owner: "michael", Repo: "homelab-gitops"})
			active, err := source.Active(t.Context())
			assert.NilError(t, err)
			assert.Equal(t, active, tc.active)
		})
	}
}
