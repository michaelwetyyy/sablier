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

var giteaActiveStatuses = map[string]struct{}{
	"queued":      {},
	"pending":     {},
	"in_progress": {},
	"running":     {},
	"waiting":     {},
}

const giteaRecentRunsLimit = "20"

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

	query := url.Values{}
	query.Set("limit", giteaRecentRunsLimit)
	query.Set("exclude_pull_requests", "false")

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
		return false, fmt.Errorf("query gitea actions: %w", err)
	}
	body, err := readJSONResponse(resp)
	if err != nil {
		return false, fmt.Errorf("query gitea actions: %w", err)
	}
	var payload giteaWorkflowRuns
	if err := json.Unmarshal(body, &payload); err != nil {
		return false, fmt.Errorf("decode gitea actions: %w", err)
	}
	for _, run := range payload.WorkflowRuns {
		if _, active := giteaActiveStatuses[run.Status]; active {
			return true, nil
		}
	}
	return false, nil
}
