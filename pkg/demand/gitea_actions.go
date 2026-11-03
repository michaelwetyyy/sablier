package demand

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type GiteaActionsSource struct {
	client       *http.Client
	baseURL      string
	tokenFile    string
	owner        string
	repo         string
	runnerLabels map[string]struct{}
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
	labels := make(map[string]struct{}, len(conf.RunnerLabels))
	for _, label := range conf.RunnerLabels {
		labels[label] = struct{}{}
	}
	return &GiteaActionsSource{
		runnerLabels: labels,
		client:       client, baseURL: strings.TrimRight(conf.URL, "/"), tokenFile: conf.TokenFile,
		owner: conf.Owner, repo: conf.Repo,
	}
}

func (s *GiteaActionsSource) Active(ctx context.Context) (bool, error) {
	token, err := tokenFromFile(s.tokenFile)
	if err != nil {
		return false, err
	}

	if len(s.runnerLabels) > 0 {
		return s.activeJobs(ctx, token)
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

// Bound each poll even if an upstream ignores pagination or its queue keeps growing.
const giteaJobsPageSize = 50
const giteaJobsMaxPages = 100

type giteaWorkflowJob struct {
	ID     int64    `json:"id"`
	Labels []string `json:"labels"`
	Status string   `json:"status"`
}

type giteaWorkflowJobs struct {
	Jobs       []giteaWorkflowJob `json:"jobs"`
	TotalCount *int               `json:"total_count"`
}

func (s *GiteaActionsSource) activeJobs(ctx context.Context, token string) (bool, error) {
	seen := make(map[int64]struct{})
	total := -1
	active := false
	// Do not follow redirects: neither API-provided URLs nor another origin may
	// receive the token. Copy the client so other demand sources are unaffected.
	client := *s.client
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	for page := 1; page <= giteaJobsMaxPages; page++ {
		query := url.Values{
			"status": {"queued", "in_progress", "pending"},
			"page":   {strconv.Itoa(page)}, "limit": {strconv.Itoa(giteaJobsPageSize)},
			"sort": {"id"}, "order": {"asc"},
		}
		endpoint := fmt.Sprintf("%s/api/v1/repos/%s/%s/actions/jobs?%s", s.baseURL, url.PathEscape(s.owner), url.PathEscape(s.repo), query.Encode())
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return false, fmt.Errorf("construct gitea jobs request")
		}
		if token != "" {
			req.Header.Set("Authorization", "token "+token)
		}
		resp, err := client.Do(req)
		if err != nil {
			return false, fmt.Errorf("query gitea jobs failed")
		}
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			_ = resp.Body.Close()
			return false, fmt.Errorf("query gitea jobs: unexpected HTTP status %d", resp.StatusCode)
		}
		body, err := readJSONResponse(resp)
		if err != nil {
			return false, fmt.Errorf("read gitea jobs response failed")
		}
		var payload giteaWorkflowJobs
		if err := json.Unmarshal(body, &payload); err != nil {
			return false, fmt.Errorf("decode gitea jobs response failed")
		}
		if payload.Jobs == nil || payload.TotalCount == nil || *payload.TotalCount < 0 || len(payload.Jobs) > giteaJobsPageSize {
			return false, fmt.Errorf("invalid gitea jobs pagination metadata")
		}
		if total == -1 {
			total = *payload.TotalCount
		} else if total != *payload.TotalCount {
			return false, fmt.Errorf("gitea jobs total changed during pagination")
		}
		for _, job := range payload.Jobs {
			if job.ID <= 0 {
				return false, fmt.Errorf("invalid gitea job ID")
			}
			if _, exists := seen[job.ID]; exists {
				return false, fmt.Errorf("repeated gitea job during pagination")
			}
			seen[job.ID] = struct{}{}
			switch job.Status {
			case "queued", "pending", "in_progress", "waiting", "blocked", "running":
				if len(job.Labels) == 0 {
					return false, fmt.Errorf("active gitea job has no runner labels")
				}
				matches := true
				for _, label := range job.Labels {
					if strings.TrimSpace(label) == "" {
						return false, fmt.Errorf("active gitea job has blank runner label")
					}
					if _, supported := s.runnerLabels[label]; !supported {
						matches = false
					}
				}
				active = active || matches
			case "success", "failure", "cancelled", "canceled", "skipped", "completed":
				// Terminal jobs never retain demand, even if returned by a racing server.
			default:
				return false, fmt.Errorf("unknown gitea job status")
			}
		}
		if len(seen) > total {
			return false, fmt.Errorf("gitea jobs count exceeds total")
		}
		if len(seen) == total {
			return active, nil
		}
		if len(payload.Jobs) < giteaJobsPageSize {
			return false, fmt.Errorf("incomplete gitea jobs pagination")
		}
	}
	return false, fmt.Errorf("gitea jobs pagination limit exceeded")
}
