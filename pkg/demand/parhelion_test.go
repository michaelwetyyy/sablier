package demand

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gotest.tools/v3/assert"
)

func TestParhelionSource(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		body   string
		active bool
	}{
		{name: "queued compatible run", body: `[{"state":"queued","constraints":{"os":"windows"}}]`, active: true},
		{name: "queued incompatible run", body: `[{"state":"queued","constraints":{"os":"linux"}}]`, active: false},
		{name: "active owned run", body: `[{"state":"running","runner_id":"windows-01","constraints":{}}]`, active: true},
		{name: "active other runner", body: `[{"state":"running","runner_id":"other","constraints":{}}]`, active: false},
		{name: "terminal runs", body: `[{"state":"succeeded","constraints":{}},{"state":"failed","constraints":{}}]`, active: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, r.URL.Path, "/api/v1/runs")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			source := NewParhelionSource(&http.Client{Timeout: time.Second}, ParhelionConfig{URL: server.URL, RunnerID: "windows-01", OS: "windows"})
			active, err := source.Active(t.Context())
			assert.NilError(t, err)
			assert.Equal(t, active, tc.active)
		})
	}
}

func TestParhelionSourceReadsRotatedToken(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "token")
	assert.NilError(t, os.WriteFile(path, []byte("first\n"), 0o600))
	seen := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()
	source := NewParhelionSource(&http.Client{Timeout: time.Second}, ParhelionConfig{URL: server.URL, TokenFile: path})
	_, err := source.Active(t.Context())
	assert.NilError(t, err)
	assert.NilError(t, os.WriteFile(path, []byte("second\n"), 0o600))
	_, err = source.Active(t.Context())
	assert.NilError(t, err)
	assert.Equal(t, <-seen, "Bearer first")
	assert.Equal(t, <-seen, "Bearer second")
}
