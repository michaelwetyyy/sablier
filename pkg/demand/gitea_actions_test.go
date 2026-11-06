package demand

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
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

func TestGiteaRunnerLabels(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, jobs        string
		active, wantError bool
	}{
		{"helper queued", `[{"id":1,"status":"waiting","labels":["validation-check"]}]`, true, false},
		{"helper running", `[{"id":1,"status":"running","labels":["validation-check"]}]`, true, false},
		{"helper in progress", `[{"id":1,"status":"in_progress","labels":["validation-check"]}]`, true, false},
		{"helper blocked", `[{"id":1,"status":"blocked","labels":["validation-check"]}]`, true, false},
		{"primary only excluded", `[{"id":1,"status":"queued","labels":["validation-primary"]}]`, false, false},
		{"all capabilities match", `[{"id":1,"status":"queued","labels":["validation-check","linux"]}]`, true, false},
		{"all labels required", `[{"id":1,"status":"queued","labels":["validation-check","special"]}]`, false, false},
		{"terminal ignored", `[{"id":1,"status":"success"}]`, false, false},
		{"missing labels", `[{"id":1,"status":"queued"}]`, false, true},
		{"empty labels", `[{"id":1,"status":"queued","labels":[]}]`, false, true},
		{"blank labels", `[{"id":1,"status":"queued","labels":[" "]}]`, false, true},
		{"malformed labels", `[{"id":1,"status":"queued","labels":"validation-check"}]`, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, r.URL.Path, "/api/v1/repos/example/repo/actions/jobs")
				assert.DeepEqual(t, r.URL.Query()["status"], []string{"queued", "in_progress", "pending"})
				assert.Equal(t, r.URL.Query().Get("sort"), "id")
				assert.Equal(t, r.URL.Query().Get("order"), "asc")
				assert.Equal(t, r.URL.Query().Get("limit"), "50")
				assert.Equal(t, r.URL.Query().Get("page"), "1")
				_, _ = fmt.Fprintf(w, `{"jobs":%s,"total_count":1}`, tc.jobs)
			}))
			defer server.Close()
			source := NewGiteaActionsSource(server.Client(), GiteaActionsConfig{URL: server.URL, Owner: "example", Repo: "repo", RunnerLabels: []string{"validation-check", "linux"}})
			active, err := source.Active(t.Context())
			assert.Equal(t, err != nil, tc.wantError)
			assert.Equal(t, active, tc.active)
		})
	}
}

func TestGiteaJobsPagination(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name              string
		mode              string
		active, wantError bool
		requests          int
	}{
		{"match after first page", "match", true, false, 2},
		{"idle only after complete scan", "idle", false, false, 2},
		{"repeated page", "repeat", false, true, 2},
		{"incomplete page", "incomplete", false, true, 2},
		{"changed total", "changed", false, true, 2},
		{"API error", "error", false, true, 2},
		{"malformed response", "malformed", false, true, 2},
		{"missing total", "missing", false, true, 2},
		{"empty queue", "empty", false, false, 1},
		{"page cap", "cap", false, true, giteaJobsMaxPages},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				page, _ := strconv.Atoi(r.URL.Query().Get("page"))
				if tc.mode == "empty" {
					_, _ = w.Write([]byte(`{"jobs":[],"total_count":0}`))
					return
				}
				if page == 2 {
					switch tc.mode {
					case "incomplete":
						_, _ = w.Write([]byte(`{"jobs":[],"total_count":51}`))
						return
					case "changed":
						_, _ = w.Write([]byte(`{"jobs":[],"total_count":50}`))
						return
					case "error":
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					case "malformed":
						_, _ = w.Write([]byte(`{`))
						return
					case "missing":
						_, _ = w.Write([]byte(`{"jobs":[]}`))
						return
					case "match":
						_, _ = w.Write([]byte(`{"jobs":[{"id":51,"status":"queued","labels":["validation-check"]}],"total_count":51}`))
						return
					case "idle":
						_, _ = w.Write([]byte(`{"jobs":[{"id":51,"status":"queued","labels":["validation-primary"]}],"total_count":51}`))
						return
					}
				}
				total := 51
				if tc.mode == "cap" {
					total = giteaJobsMaxPages*giteaJobsPageSize + 1
				}
				jobs := make([]map[string]any, giteaJobsPageSize)
				for i := range jobs {
					id := i + 1
					if tc.mode == "cap" {
						id += (page - 1) * giteaJobsPageSize
					}
					jobs[i] = map[string]any{"id": id, "status": "queued", "labels": []string{"validation-primary"}}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"jobs": jobs, "total_count": total})
			}))
			defer server.Close()
			source := NewGiteaActionsSource(server.Client(), GiteaActionsConfig{URL: server.URL, Owner: "example", Repo: "repo", RunnerLabels: []string{"validation-check"}})
			active, err := source.Active(t.Context())
			assert.Equal(t, err != nil, tc.wantError)
			assert.Equal(t, active, tc.active)
			assert.Equal(t, requests, tc.requests)
		})
	}
}

func TestGiteaJobsDoesNotFollowRedirects(t *testing.T) {
	t.Parallel()
	var followed atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { followed.Add(1) }))
	defer destination.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, http.StatusFound) }))
	defer server.Close()
	source := NewGiteaActionsSource(server.Client(), GiteaActionsConfig{URL: server.URL, Owner: "example", Repo: "repo", RunnerLabels: []string{"validation-check"}})
	active, err := source.Active(t.Context())
	assert.Assert(t, err != nil)
	assert.Equal(t, active, false)
	assert.Equal(t, followed.Load(), int32(0))
}

func TestGiteaJobsTokenRotation(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "token")
	expected := "first-synthetic-token"
	assert.NilError(t, os.WriteFile(path, []byte(expected), 0600))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token "+expected {
			t.Error("token file was not reloaded")
		}
		_, _ = w.Write([]byte(`{"jobs":[],"total_count":0}`))
	}))
	defer server.Close()
	source := NewGiteaActionsSource(server.Client(), GiteaActionsConfig{URL: server.URL, Owner: "example", Repo: "repo", TokenFile: path, RunnerLabels: []string{"validation-check"}})
	for _, token := range []string{"first-synthetic-token", "rotated-synthetic-token"} {
		expected = token
		assert.NilError(t, os.WriteFile(path, []byte(token), 0600))
		active, err := source.Active(t.Context())
		assert.NilError(t, err)
		assert.Equal(t, active, false)
	}
}
