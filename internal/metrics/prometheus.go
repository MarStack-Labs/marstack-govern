package metrics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var ErrUnavailable = errors.New("no metrics source is configured")

type Client struct {
	baseURL string
	tenant  string
	http    *http.Client
}

type Config struct {
	BaseURL string
	Tenant  string
	Timeout time.Duration
}

func New(cfg Config) *Client {
	if cfg.BaseURL == "" {
		return nil
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}

	return &Client{
		baseURL: strings.TrimSuffix(cfg.BaseURL, "/"),
		tenant:  cfg.Tenant,
		http:    &http.Client{Timeout: timeout},
	}
}

func (c *Client) Available() bool {
	return c != nil && c.baseURL != ""
}

func (c *Client) Scalar(ctx context.Context, query string) (float64, bool, error) {
	return c.ScalarAt(ctx, query, time.Time{})
}

func (c *Client) ScalarAt(ctx context.Context, query string, at time.Time) (float64, bool, error) {
	if !c.Available() {
		return 0, false, ErrUnavailable
	}

	values := url.Values{"query": {query}}
	if !at.IsZero() {
		values.Set("time", strconv.FormatInt(at.Unix(), 10))
	}

	endpoint := c.baseURL + "/api/v1/query?" + values.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, false, fmt.Errorf("build metrics request: %w", err)
	}
	if c.tenant != "" {
		request.Header.Set("X-Scope-OrgID", c.tenant)
	}

	response, err := c.http.Do(request)
	if err != nil {
		return 0, false, fmt.Errorf("query metrics: %w", err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		return 0, false, fmt.Errorf("metrics query answered %s", response.Status)
	}

	var payload struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Value []any `json:"value"`
			} `json:"result"`
		} `json:"data"`
		Error string `json:"error"`
	}

	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return 0, false, fmt.Errorf("decode metrics answer: %w", err)
	}

	if payload.Status != "success" {
		return 0, false, fmt.Errorf("metrics query failed: %s", payload.Error)
	}

	if len(payload.Data.Result) == 0 {
		return 0, false, nil
	}

	value := payload.Data.Result[0].Value
	if len(value) != 2 {
		return 0, false, fmt.Errorf("metrics answer carries no sample")
	}

	raw, ok := value[1].(string)
	if !ok {
		return 0, false, fmt.Errorf("metrics sample is not a string")
	}

	parsed, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, false, fmt.Errorf("parse metrics sample %q: %w", raw, err)
	}

	return parsed, true, nil
}
