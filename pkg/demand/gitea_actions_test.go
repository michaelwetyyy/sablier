package demand

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"gotest.tools/v3/assert"
)

func TestGiteaActionsSource(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		runs   string
		active bool
	}{
		{name: "queued", runs: `[{"status":"completed"},{"status":"queued"}]`, active: true},
		{name: "pending", runs: `[{"status":"pending"}]`, active: true},
		{name: "in progress", runs: `[{"status":"in_progress"}]`, active: true},
		{name: "running", runs: `[{"status":"running"}]`, active: true},
		{name: "waiting", runs: `[{"status":"waiting"}]`, active: true},
		{name: "idle", runs: `[{"status":"completed"},{"status":"failure"}]`, active: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				assert.Equal(t, r.URL.Path, "/api/v1/repos/michael/homelab-gitops/actions/runs")
				assert.Equal(t, r.URL.Query().Get("limit"), giteaRecentRunsLimit)
				assert.Equal(t, r.URL.Query().Get("exclude_pull_requests"), "false")
				assert.Equal(t, r.URL.Query().Get("status"), "")
				_, _ = w.Write([]byte(`{"workflow_runs":` + tc.runs + `}`))
			}))
			defer server.Close()

			source := NewGiteaActionsSource(&http.Client{Timeout: time.Second}, GiteaActionsConfig{
				URL: server.URL, Owner: "michael", Repo: "homelab-gitops",
			})
			active, err := source.Active(t.Context())
			assert.NilError(t, err)
			assert.Equal(t, active, tc.active)
			assert.Equal(t, requests.Load(), int32(1))
		})
	}
}

func TestGiteaActionsSourceReturnsError(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	source := NewGiteaActionsSource(&http.Client{Timeout: time.Second}, GiteaActionsConfig{
		URL: server.URL, Owner: "michael", Repo: "homelab-gitops",
	})
	active, err := source.Active(t.Context())
	assert.Assert(t, err != nil)
	assert.Equal(t, active, false)
	assert.Equal(t, requests.Load(), int32(1))
}
