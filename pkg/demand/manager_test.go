package demand

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"gotest.tools/v3/assert"
)

type fakeSource struct {
	active bool
	err    error
}

func (f fakeSource) Active(context.Context) (bool, error) { return f.active, f.err }

type pokeCall struct {
	target   TargetConfig
	duration time.Duration
}

type fakePoker struct {
	mu    sync.Mutex
	calls []pokeCall
}

func (f *fakePoker) Poke(_ context.Context, target TargetConfig, duration time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, pokeCall{target: target, duration: duration})
	return nil
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestManagerReconcile(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		source        Source
		failurePolicy string
		wantPokes     int
	}{
		{name: "active renews", source: fakeSource{active: true}, failurePolicy: FailurePolicyAwake, wantPokes: 1},
		{name: "idle expires naturally", source: fakeSource{}, failurePolicy: FailurePolicyAwake, wantPokes: 0},
		{name: "source error fails awake", source: fakeSource{err: errors.New("down")}, failurePolicy: FailurePolicyAwake, wantPokes: 1},
		{name: "source error can ignore", source: fakeSource{err: errors.New("down")}, failurePolicy: FailurePolicyIgnore, wantPokes: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			poker := &fakePoker{}
			manager := NewManagerWithSources(time.Second, poker, []NamedSource{{
				Config: SourceConfig{Name: "test", Type: "test", FailurePolicy: tc.failurePolicy, IdleAfter: 10 * time.Minute, Target: TargetConfig{Group: "target"}},
				Source: tc.source,
			}}, testLogger())
			manager.reconcile(t.Context(), manager.sources[0])
			assert.Equal(t, len(poker.calls), tc.wantPokes)
			if tc.wantPokes == 1 {
				assert.Equal(t, poker.calls[0].duration, 10*time.Minute)
				assert.Equal(t, poker.calls[0].target.Group, "target")
			}
		})
	}
}
