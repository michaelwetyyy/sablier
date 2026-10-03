package demand

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gotest.tools/v3/assert"
)

func TestSablierClientPoke(t *testing.T) {
	t.Parallel()
	var gotGroup, gotDuration string
	var gotNames []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, r.URL.Path, "/api/strategies/poke")
		gotGroup = r.URL.Query().Get("group")
		gotNames = r.URL.Query()["names"]
		gotDuration = r.URL.Query().Get("session_duration")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"session":{}}`))
	}))
	defer server.Close()
	client := NewSablierClient(&http.Client{Timeout: time.Second}, server.URL)
	assert.NilError(t, client.Poke(t.Context(), TargetConfig{Group: "parhelion"}, 30*time.Minute))
	assert.Equal(t, gotGroup, "parhelion")
	assert.Equal(t, gotDuration, "30m0s")
	assert.Equal(t, len(gotNames), 0)

	assert.NilError(t, client.Poke(t.Context(), TargetConfig{Names: []string{"one", "two"}}, 10*time.Minute))
	assert.DeepEqual(t, gotNames, []string{"one", "two"})
}
