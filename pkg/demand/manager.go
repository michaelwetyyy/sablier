package demand

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

type sourceState struct {
	known  bool
	active bool
}

type Manager struct {
	pollInterval time.Duration
	poker        Poker
	sources      []NamedSource
	logger       *slog.Logger
	metrics      *Metrics
	stateMu      sync.Mutex
	states       map[string]sourceState
}

func NewManager(conf Config, logger *slog.Logger) (*Manager, error) {
	client := &http.Client{Timeout: conf.RequestTimeout}
	sources := make([]NamedSource, 0, len(conf.Sources))
	for _, sourceConf := range conf.Sources {
		var source Source
		switch sourceConf.Type {
		case SourceTypeParhelion:
			source = NewParhelionSource(client, *sourceConf.Parhelion)
		case SourceTypeGiteaActions:
			source = NewGiteaActionsSource(client, *sourceConf.GiteaActions)
		default:
			return nil, fmt.Errorf("unsupported demand source type %q", sourceConf.Type)
		}
		sources = append(sources, NamedSource{Config: sourceConf, Source: source})
	}
	return &Manager{
		pollInterval: conf.PollInterval,
		poker:        NewSablierClient(client, conf.Sablier.URL),
		sources:      sources,
		logger:       logger,
		metrics:      NewMetrics(conf),
		states:       make(map[string]sourceState, len(sources)),
	}, nil
}

func NewManagerWithSources(pollInterval time.Duration, poker Poker, sources []NamedSource, logger *slog.Logger) *Manager {
	return &Manager{pollInterval: pollInterval, poker: poker, sources: sources, logger: logger, states: make(map[string]sourceState, len(sources))}
}

func (m *Manager) MetricsHandler() http.Handler {
	if m == nil || m.metrics == nil {
		return http.NotFoundHandler()
	}
	return m.metrics.Handler()
}

func (m *Manager) Run(ctx context.Context) error {
	if len(m.sources) == 0 {
		return fmt.Errorf("demand manager has no sources")
	}
	var wg sync.WaitGroup
	for _, source := range m.sources {
		source := source
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.runSource(ctx, source)
		}()
	}
	<-ctx.Done()
	wg.Wait()
	return nil
}

func (m *Manager) runSource(ctx context.Context, source NamedSource) {
	m.reconcile(ctx, source)
	ticker := time.NewTicker(m.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.reconcile(ctx, source)
		}
	}
}

func (m *Manager) reconcile(ctx context.Context, source NamedSource) {
	active, err := source.Source.Active(ctx)
	checkErr := err
	if err != nil {
		m.logger.ErrorContext(ctx, "demand source check failed",
			slog.String("source", source.Config.Name), slog.String("type", source.Config.Type), slog.Any("error", err))
		switch source.Config.FailurePolicy {
		case FailurePolicyAwake:
			active = true
		case FailurePolicyIgnore:
			m.metrics.ObserveSource(source.Config, false, checkErr)
			return
		case FailurePolicyLastKnown:
			state := m.sourceState(source.Config.Name)
			if !state.known {
				// Until a source has completed one successful check, prefer an
				// unnecessary wake over stranding work behind a sleeping target.
				active = true
			} else {
				active = state.active
			}
		default:
			// Config validation rejects unknown policies. Keep a conservative
			// runtime fallback for callers using NewManagerWithSources directly.
			active = true
		}
	} else {
		m.setSourceState(source.Config.Name, active)
	}
	m.metrics.ObserveSource(source.Config, active, checkErr)
	if !active {
		m.logger.DebugContext(ctx, "demand source idle", slog.String("source", source.Config.Name))
		return
	}
	if err := m.poker.Poke(ctx, source.Config.Target, source.Config.IdleAfter); err != nil {
		m.logger.ErrorContext(ctx, "failed to renew sablier demand session",
			slog.String("source", source.Config.Name), slog.Any("error", err))
		return
	}
	m.logger.DebugContext(ctx, "renewed sablier demand session",
		slog.String("source", source.Config.Name), slog.Duration("idle_after", source.Config.IdleAfter))
}

func (m *Manager) sourceState(name string) sourceState {
	m.stateMu.Lock()
	defer m.stateMu.Unlock()
	return m.states[name]
}

func (m *Manager) setSourceState(name string, active bool) {
	m.stateMu.Lock()
	defer m.stateMu.Unlock()
	if m.states == nil {
		m.states = make(map[string]sourceState)
	}
	m.states[name] = sourceState{known: true, active: active}
}
