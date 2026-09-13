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

type Sample struct {
	Labels map[string]string
	Value  float64
}

func (c *Client) Scalar(ctx context.Context, query string) (float64, bool, error) {
	return c.ScalarAt(ctx, query, time.Time{})
}

func (c *Client) Vector(ctx context.Context, query string) ([]Sample, error) {
	if !c.Available() {
		return nil, ErrUnavailable
	}

	payload, err := c.query(ctx, query, time.Time{})
	if err != nil {
		return nil, err
	}

	samples := make([]Sample, 0, len(payload.Data.Result))

	for _, result := range payload.Data.Result {
		value, err := sampleValue(result.Value)
		if err != nil {
			return nil, err
		}

		samples = append(samples, Sample{Labels: result.Metric, Value: value})
	}

	return samples, nil
}

func (c *Client) ScalarAt(ctx context.Context, query string, at time.Time) (float64, bool, error) {
	if !c.Available() {
		return 0, false, ErrUnavailable
	}

	payload, err := c.query(ctx, query, at)
	if err != nil {
		return 0, false, err
	}

	if len(payload.Data.Result) == 0 {
		return 0, false, nil
	}

	value, err := sampleValue(payload.Data.Result[0].Value)
	if err != nil {
		return 0, false, err
	}

	return value, true, nil
}

type answer struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Value  []any             `json:"value"`
		} `json:"result"`
	} `json:"data"`
	Error string `json:"error"`
}

func (c *Client) query(ctx context.Context, query string, at time.Time) (answer, error) {
	values := url.Values{"query": {query}}
	if !at.IsZero() {
		values.Set("time", strconv.FormatInt(at.Unix(), 10))
	}

	endpoint := c.baseURL + "/api/v1/query?" + values.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return answer{}, fmt.Errorf("build metrics request: %w", err)
	}
	if c.tenant != "" {
		request.Header.Set("X-Scope-OrgID", c.tenant)
	}

	response, err := c.http.Do(request)
	if err != nil {
		return answer{}, fmt.Errorf("query metrics: %w", err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		return answer{}, fmt.Errorf("metrics query answered %s", response.Status)
	}

	var payload answer
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return answer{}, fmt.Errorf("decode metrics answer: %w", err)
	}

	if payload.Status != "success" {
		return answer{}, fmt.Errorf("metrics query failed: %s", payload.Error)
	}

	return payload, nil
}

func sampleValue(value []any) (float64, error) {
	if len(value) != 2 {
		return 0, fmt.Errorf("metrics answer carries no sample")
	}

	raw, ok := value[1].(string)
	if !ok {
		return 0, fmt.Errorf("metrics sample is not a string")
	}

	parsed, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("parse metrics sample %q: %w", raw, err)
	}

	return parsed, nil
}
