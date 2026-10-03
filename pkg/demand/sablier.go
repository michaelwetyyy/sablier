package demand

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Poker interface {
	Poke(context.Context, TargetConfig, time.Duration) error
}

type SablierClient struct {
	client  *http.Client
	baseURL string
}

func NewSablierClient(client *http.Client, baseURL string) *SablierClient {
	return &SablierClient{client: client, baseURL: strings.TrimRight(baseURL, "/")}
}

func (c *SablierClient) Poke(ctx context.Context, target TargetConfig, duration time.Duration) error {
	endpoint, err := url.Parse(c.baseURL + "/api/strategies/poke")
	if err != nil {
		return err
	}
	query := endpoint.Query()
	if target.Group != "" {
		query.Set("group", target.Group)
	} else {
		for _, name := range target.Names {
			query.Add("names", name)
		}
	}
	query.Set("session_duration", duration.String())
	endpoint.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("poke sablier: %w", err)
	}
	_, err = readJSONResponse(resp)
	if err != nil {
		return fmt.Errorf("poke sablier: %w", err)
	}
	return nil
}
