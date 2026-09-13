package topology

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/marstack-labs/marstack-govern/internal/metrics"
)

var ErrNoTraces = errors.New("no trace metrics are available, so the graph would be a guess")

type Edge struct {
	Client        string
	Server        string
	RequestsPerS  float64
	FailedPerS    float64
	ClientDivided bool
}

func (e Edge) ErrorRatio() float64 {
	if e.RequestsPerS <= 0 {
		return 0
	}

	return e.FailedPerS / e.RequestsPerS
}

type Vector interface {
	Vector(ctx context.Context, query string) ([]metrics.Sample, error)
	Available() bool
}

type Graph struct {
	source Vector
	window time.Duration
}

func NewGraph(source Vector, window time.Duration) *Graph {
	if window <= 0 {
		window = time.Hour
	}

	return &Graph{source: source, window: window}
}

func (g *Graph) Available() bool {
	return g != nil && g.source != nil && g.source.Available()
}

func (g *Graph) Edges(ctx context.Context, namespaces []string) ([]Edge, error) {
	if !g.Available() {
		return nil, ErrNoTraces
	}

	totals, err := g.rates(ctx, "traces_service_graph_request_total", namespaces)
	if err != nil {
		return nil, err
	}

	failures, err := g.rates(ctx, "traces_service_graph_request_failed_total", namespaces)
	if err != nil {
		return nil, err
	}

	if len(totals) == 0 {
		return nil, ErrNoTraces
	}

	edges := make([]Edge, 0, len(totals))
	for key, rate := range totals {
		client, server, _ := strings.Cut(key, "\x00")

		edges = append(edges, Edge{
			Client:       client,
			Server:       server,
			RequestsPerS: rate,
			FailedPerS:   failures[key],
		})
	}

	sort.SliceStable(edges, func(i, j int) bool {
		if edges[i].Client != edges[j].Client {
			return edges[i].Client < edges[j].Client
		}
		return edges[i].Server < edges[j].Server
	})

	return edges, nil
}

func (g *Graph) rates(ctx context.Context, metric string, namespaces []string) (map[string]float64, error) {
	query := fmt.Sprintf(`sum by (client, server) (rate(%s{%s}[%s]))`,
		metric, selector(namespaces), promDuration(g.window))

	samples, err := g.source.Vector(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", metric, err)
	}

	rates := map[string]float64{}
	for _, sample := range samples {
		client := sample.Labels["client"]
		server := sample.Labels["server"]
		if client == "" || server == "" {
			continue
		}

		rates[client+"\x00"+server] += sample.Value
	}

	return rates, nil
}

func selector(namespaces []string) string {
	if len(namespaces) == 0 {
		return ""
	}

	escaped := make([]string, 0, len(namespaces))
	for _, namespace := range namespaces {
		escaped = append(escaped, strings.NewReplacer(`\`, `\\`, `|`, `\|`, `.`, `\.`).Replace(namespace))
	}

	return fmt.Sprintf(`client_namespace=~"%s"`, strings.Join(escaped, "|"))
}

func promDuration(window time.Duration) string {
	minutes := int(window.Minutes())
	if minutes <= 0 {
		minutes = 1
	}

	return fmt.Sprintf("%dm", minutes)
}
