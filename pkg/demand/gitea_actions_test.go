package demand

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"gotest.tools/v3/assert"
)

func TestGiteaActionsSource(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name         string
		activeStatus string
		active       bool
	}{
		{name: "queued", activeStatus: "queued", active: true},
		{name: "pending", activeStatus: "pending", active: true},
		{name: "in progress", activeStatus: "in_progress", active: true},
		{name: "idle", activeStatus: "", active: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var mu sync.Mutex
			seen := map[string]bool{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, r.URL.Path, "/api/v1/repos/michael/homelab-gitops/actions/runs")
				assert.Equal(t, r.URL.Query().Get("limit"), "1")
				assert.Equal(t, r.URL.Query().Get("exclude_pull_requests"), "true")
				status := r.URL.Query().Get("status")
				assert.Assert(t, status == "queued" || status == "pending" || status == "in_progress")
				mu.Lock()
				seen[status] = true
				mu.Unlock()
				if status == tc.activeStatus {
					_, _ = w.Write([]byte(`{"workflow_runs":[{"status":"` + status + `"}]}`))
					return
				}
				_, _ = w.Write([]byte(`{"workflow_runs":[]}`))
			}))
			defer server.Close()

			source := NewGiteaActionsSource(&http.Client{Timeout: time.Second}, GiteaActionsConfig{
				URL: server.URL, Owner: "michael", Repo: "homelab-gitops",
			})
			active, err := source.Active(t.Context())
			assert.NilError(t, err)
			assert.Equal(t, active, tc.active)

			mu.Lock()
			defer mu.Unlock()
			assert.Assert(t, len(seen) > 0)
		})
	}
}

func TestGiteaActionsSourceReturnsErrorWhenNoActiveStatusSucceeds(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	source := NewGiteaActionsSource(&http.Client{Timeout: time.Second}, GiteaActionsConfig{
		URL: server.URL, Owner: "michael", Repo: "homelab-gitops",
	})
	active, err := source.Active(t.Context())
	assert.Assert(t, err != nil)
	assert.Equal(t, active, false)
}
