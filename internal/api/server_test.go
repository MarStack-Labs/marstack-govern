package api

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	governv1 "github.com/marstack-labs/marstack-govern/gen/marstack/govern/v1"
	"github.com/marstack-labs/marstack-govern/internal/catalog"
	"github.com/marstack-labs/marstack-govern/internal/identity"
	"github.com/marstack-labs/marstack-govern/internal/web"
)

func TestHealthzReportsTheVersion(t *testing.T) {
	server := httptest.NewServer(NewHandler(Options{}))
	defer server.Close()

	response, err := server.Client().Get(server.URL + "/healthz")
	if err != nil {
		t.Fatalf("get healthz: %v", err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", response.StatusCode)
	}

	body := readAll(t, response.Body)
	if !strings.Contains(body, `"status":"ok"`) {
		t.Fatalf("body: %s", body)
	}
}

func TestTheUiIsEmbeddedInTheBinary(t *testing.T) {
	assets, err := web.Assets()
	if err != nil {
		t.Fatalf("open assets: %v", err)
	}

	server := httptest.NewServer(NewHandler(Options{Web: assets}))
	defer server.Close()

	response, err := server.Client().Get(server.URL + "/")
	if err != nil {
		t.Fatalf("get index: %v", err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", response.StatusCode)
	}

	body := readAll(t, response.Body)
	if !strings.Contains(body, `id="root"`) {
		t.Fatalf("the served page is not the built ui: %s", body)
	}
}

func TestUnknownPathsFallBackToTheApp(t *testing.T) {
	assets, err := web.Assets()
	if err != nil {
		t.Fatalf("open assets: %v", err)
	}

	server := httptest.NewServer(NewHandler(Options{Web: assets}))
	defer server.Close()

	response, err := server.Client().Get(server.URL + "/services/some-uid")
	if err != nil {
		t.Fatalf("get deep link: %v", err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", response.StatusCode)
	}
	if body := readAll(t, response.Body); !strings.Contains(body, `id="root"`) {
		t.Fatalf("a deep link did not fall back to the app: %s", body)
	}
}

func TestEventStreamEmitsFramesWithCursors(t *testing.T) {
	hub := NewHub(16)
	server := httptest.NewServer(NewHandler(Options{Hub: hub, Heartbeat: time.Hour}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/v1/events", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}

	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer func() { _ = response.Body.Close() }()

	if contentType := response.Header.Get("Content-Type"); contentType != "text/event-stream" {
		t.Fatalf("content type: got %q", contentType)
	}

	waitForSubscriber(t, hub)

	hub.Publish(&governv1.StreamEvent{
		Type: governv1.StreamEvent_TYPE_WORKLOAD_CHANGED,
		Body: &governv1.StreamEvent_WorkloadChanged{
			WorkloadChanged: &governv1.WorkloadChanged{
				Change:   governv1.WorkloadChanged_CHANGE_ADDED,
				Workload: &governv1.Workload{Name: "api", Namespace: "payments-dev"},
			},
		},
	})

	frame := readFrame(t, response.Body)

	if !strings.Contains(frame, "id: 1") {
		t.Errorf("frame carries no cursor: %s", frame)
	}
	if !strings.Contains(frame, "event: workload_changed") {
		t.Errorf("frame carries the wrong event name: %s", frame)
	}
	if !strings.Contains(frame, `"name":"api"`) {
		t.Errorf("frame carries no payload: %s", frame)
	}
}

func TestEventStreamAsksForAResyncWhenTheCursorIsTooOld(t *testing.T) {
	hub := NewHub(2)
	for range 5 {
		hub.Publish(&governv1.StreamEvent{Type: governv1.StreamEvent_TYPE_HEARTBEAT})
	}

	server := httptest.NewServer(NewHandler(Options{Hub: hub, Heartbeat: time.Hour}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/v1/events", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Last-Event-ID", "1")

	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer func() { _ = response.Body.Close() }()

	if frame := readFrame(t, response.Body); !strings.Contains(frame, "event: resync") {
		t.Fatalf("the client was not told to resync: %s", frame)
	}
}

func waitForSubscriber(t *testing.T, hub *Hub) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for hub.Subscribers() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the stream never registered a subscriber")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func readFrame(t *testing.T, body io.Reader) string {
	t.Helper()

	reader := bufio.NewReader(body)
	frame := strings.Builder{}

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		if line == "\n" {
			return frame.String()
		}
		frame.WriteString(line)
	}
}

func readAll(t *testing.T, body io.Reader) string {
	t.Helper()

	raw, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	return string(raw)
}

func TestRpcsRefuseAnonymousCallersWhenSessionsAreOn(t *testing.T) {
	key, err := identity.NewKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	sealer, err := identity.NewSealer(key, false)
	if err != nil {
		t.Fatalf("build sealer: %v", err)
	}

	server := httptest.NewServer(NewHandler(Options{
		Catalog: catalog.NewService(nil),
		Sealer:  sealer,
	}))
	defer server.Close()

	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		server.URL+"/marstack.govern.v1.CatalogService/ListWorkloads",
		strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("call rpc: %v", err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want 401 (%s)", response.StatusCode, readAll(t, response.Body))
	}
}
