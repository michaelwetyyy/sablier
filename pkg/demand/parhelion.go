package demand

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type ParhelionSource struct {
	client    *http.Client
	baseURL   string
	tokenFile string
	runnerID  string
	os        string
}

type parhelionRun struct {
	State       string  `json:"state"`
	RunnerID    *string `json:"runner_id"`
	Constraints struct {
		OS *string `json:"os"`
	} `json:"constraints"`
}

func NewParhelionSource(client *http.Client, conf ParhelionConfig) *ParhelionSource {
	return &ParhelionSource{
		client: client, baseURL: strings.TrimRight(conf.URL, "/"), tokenFile: conf.TokenFile,
		runnerID: conf.RunnerID, os: conf.OS,
	}
}

func (s *ParhelionSource) Active(ctx context.Context) (bool, error) {
	token, err := tokenFromFile(s.tokenFile)
	if err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+"/api/v1/runs", nil)
	if err != nil {
		return false, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("query parhelion runs: %w", err)
	}
	body, err := readJSONResponse(resp)
	if err != nil {
		return false, fmt.Errorf("query parhelion runs: %w", err)
	}
	var runs []parhelionRun
	if err := json.Unmarshal(body, &runs); err != nil {
		return false, fmt.Errorf("decode parhelion runs: %w", err)
	}
	for _, run := range runs {
		switch run.State {
		case "queued":
			if s.os == "" || run.Constraints.OS == nil || *run.Constraints.OS == s.os {
				return true, nil
			}
		case "assigned", "running", "needs_attention":
			if s.runnerID == "" || run.RunnerID == nil || *run.RunnerID == s.runnerID {
				return true, nil
			}
		}
	}
	return false, nil
}
