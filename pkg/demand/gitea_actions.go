package demand

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

type GiteaActionsSource struct {
	client    *http.Client
	baseURL   string
	tokenFile string
	owner     string
	repo      string
}

type giteaWorkflowRuns struct {
	WorkflowRuns []struct {
		Status string `json:"status"`
	} `json:"workflow_runs"`
}

type giteaActionsResult struct {
	active bool
	err    error
}

var giteaActiveStatuses = []string{"queued", "pending", "in_progress"}

func NewGiteaActionsSource(client *http.Client, conf GiteaActionsConfig) *GiteaActionsSource {
	return &GiteaActionsSource{
		client: client, baseURL: strings.TrimRight(conf.URL, "/"), tokenFile: conf.TokenFile,
		owner: conf.Owner, repo: conf.Repo,
	}
}

func (s *GiteaActionsSource) Active(ctx context.Context) (bool, error) {
	token, err := tokenFromFile(s.tokenFile)
	if err != nil {
		return false, err
	}

	// Gitea's unfiltered Actions runs endpoint becomes expensive on repositories
	// with significant run history. We only care whether runnable work exists, so
	// ask for one run from each non-terminal state instead of fetching a page of
	// mixed historical runs and filtering it client-side.
	queryCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make(chan giteaActionsResult, len(giteaActiveStatuses))
	for _, status := range giteaActiveStatuses {
		status := status
		go func() {
			active, err := s.statusActive(queryCtx, token, status)
			results <- giteaActionsResult{active: active, err: err}
		}()
	}

	var firstErr error
	for range giteaActiveStatuses {
		result := <-results
		if result.active {
			cancel()
			return true, nil
		}
		if result.err != nil && firstErr == nil {
			firstErr = result.err
		}
	}
	if firstErr != nil {
		return false, firstErr
	}
	return false, nil
}

func (s *GiteaActionsSource) statusActive(ctx context.Context, token, status string) (bool, error) {
	query := url.Values{}
	query.Set("status", status)
	query.Set("limit", "1")
	query.Set("exclude_pull_requests", "true")

	endpoint := fmt.Sprintf(
		"%s/api/v1/repos/%s/%s/actions/runs?%s",
		s.baseURL,
		url.PathEscape(s.owner),
		url.PathEscape(s.repo),
		query.Encode(),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false, err
	}
	if token != "" {
		req.Header.Set("Authorization", "token "+token)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("query gitea actions status %s: %w", status, err)
	}
	body, err := readJSONResponse(resp)
	if err != nil {
		return false, fmt.Errorf("query gitea actions status %s: %w", status, err)
	}
	var payload giteaWorkflowRuns
	if err := json.Unmarshal(body, &payload); err != nil {
		return false, fmt.Errorf("decode gitea actions status %s: %w", status, err)
	}
	return len(payload.WorkflowRuns) > 0, nil
}
