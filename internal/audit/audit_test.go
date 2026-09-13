package audit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgxpool"

	governv1 "github.com/marstack-labs/marstack-govern/gen/marstack/govern/v1"
	"github.com/marstack-labs/marstack-govern/internal/audit"
	"github.com/marstack-labs/marstack-govern/internal/db/dbtest"
	"github.com/marstack-labs/marstack-govern/internal/identity"
)

const token = "a-shared-secret"

func TestTheWebhookNeedsItsToken(t *testing.T) {
	pool := dbtest.Migrated(t)
	server := newReceiver(t, pool, nil)

	response := post(t, server, "", batch(quotaRequestEvent("1", "dev@example.test", "")))
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status without a token: got %d, want 401", response.StatusCode)
	}

	wrong := post(t, server, "not-the-secret", batch(quotaRequestEvent("1", "dev@example.test", "")))
	if wrong.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status with a wrong token: got %d, want 401", wrong.StatusCode)
	}
}

func TestEventsAreRecordedAndChained(t *testing.T) {
	pool := dbtest.Migrated(t)
	server := newReceiver(t, pool, nil)

	response := post(t, server, token, batch(
		quotaRequestEvent("1", "dev@example.test", ""),
		quotaRequestEvent("2", "lead@example.test", ""),
	))
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", response.StatusCode)
	}

	store := audit.NewStore(pool)

	records, start, err := store.Range(t.Context(), 1, 0)
	if err != nil {
		t.Fatalf("range: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("recorded %d events, want 2", len(records))
	}

	events := []audit.Event{records[0].Event, records[1].Event}
	verification := audit.Verify(events, start)

	if !verification.Intact {
		t.Fatalf("a freshly written chain does not verify: %s", verification.Detail)
	}
	if verification.Checked != 2 {
		t.Errorf("checked %d events", verification.Checked)
	}
}

func TestTamperingWithAPayloadBreaksTheChain(t *testing.T) {
	pool := dbtest.Migrated(t)
	server := newReceiver(t, pool, nil)

	post(t, server, token, batch(
		quotaRequestEvent("1", "dev@example.test", ""),
		quotaRequestEvent("2", "lead@example.test", ""),
		quotaRequestEvent("3", "lead@example.test", ""),
	))

	store := audit.NewStore(pool)
	records, start, err := store.Range(t.Context(), 1, 0)
	if err != nil {
		t.Fatalf("range: %v", err)
	}

	events := []audit.Event{records[0].Event, records[1].Event, records[2].Event}
	events[1].Actor = "someone-else@example.test"

	verification := audit.Verify(events, start)

	if verification.Intact {
		t.Fatal("an altered event still verified")
	}
	if verification.BrokenAtSeq != 1 {
		t.Errorf("broken at %d, want the altered second event", verification.BrokenAtSeq)
	}
}

func TestTheDatabaseRefusesToRewriteHistory(t *testing.T) {
	pool := dbtest.Migrated(t)
	server := newReceiver(t, pool, nil)

	post(t, server, token, batch(quotaRequestEvent("1", "dev@example.test", "")))

	if _, err := pool.Exec(t.Context(), `UPDATE audit_events SET actor = 'forged'`); err == nil {
		t.Fatal("an update to the audit table was accepted")
	}
	if _, err := pool.Exec(t.Context(), `DELETE FROM audit_events`); err == nil {
		t.Fatal("a delete from the audit table was accepted")
	}
}

func TestImpersonationRecordsTheHumanNotTheServiceAccount(t *testing.T) {
	pool := dbtest.Migrated(t)
	server := newReceiver(t, pool, nil)

	post(t, server, token, batch(quotaRequestEvent("1", "system:serviceaccount:govern:margov", "dev@example.test")))

	records, _, err := audit.NewStore(pool).Range(t.Context(), 1, 0)
	if err != nil {
		t.Fatalf("range: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("recorded %d events", len(records))
	}

	if records[0].Actor != "dev@example.test" {
		t.Errorf("actor: got %q, want the impersonated human", records[0].Actor)
	}
	if records[0].ImpersonatedBy != "system:serviceaccount:govern:margov" {
		t.Errorf("impersonated by: got %q", records[0].ImpersonatedBy)
	}
}

func TestNoiseIsFilteredOut(t *testing.T) {
	pool := dbtest.Migrated(t)
	server := newReceiver(t, pool, nil)

	noise := quotaRequestEvent("1", "dev@example.test", "")
	noise["objectRef"] = map[string]any{"resource": "events", "apiGroup": "", "namespace": "default"}

	stage := quotaRequestEvent("2", "dev@example.test", "")
	stage["stage"] = "RequestReceived"

	post(t, server, token, batch(noise, stage, quotaRequestEvent("3", "dev@example.test", "")))

	records, _, err := audit.NewStore(pool).Range(t.Context(), 1, 0)
	if err != nil {
		t.Fatalf("range: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("recorded %d events, want only the governance one", len(records))
	}
	if records[0].AuditID != "3" {
		t.Errorf("recorded %q", records[0].AuditID)
	}
}

func TestTheArchiveKeepsACopyThatVerificationCompares(t *testing.T) {
	pool := dbtest.Migrated(t)

	archive, err := audit.NewFileArchive(t.TempDir())
	if err != nil {
		t.Fatalf("archive: %v", err)
	}

	server := newReceiver(t, pool, archive)
	post(t, server, token, batch(quotaRequestEvent("1", "dev@example.test", "")))

	head, err := archive.Head()
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if len(head) != 32 {
		t.Fatalf("the archive head is %d bytes, want a sha256", len(head))
	}

	service := audit.NewService(audit.NewStore(pool), archive)

	response, err := service.VerifyChain(signedIn(t), connect.NewRequest(&governv1.VerifyChainRequest{
		CompareArchive: true,
	}))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}

	verification := response.Msg.GetVerification()
	if !verification.GetIntact() {
		t.Fatalf("verification failed: %s", verification.GetDetail())
	}
	if !verification.GetArchiveCompared() {
		t.Error("the archive was not compared")
	}
}

func TestAnEventMissingFromTheArchiveIsReported(t *testing.T) {
	pool := dbtest.Migrated(t)

	archive, err := audit.NewFileArchive(t.TempDir())
	if err != nil {
		t.Fatalf("archive: %v", err)
	}

	unarchived := newReceiver(t, pool, nil)
	post(t, unarchived, token, batch(quotaRequestEvent("1", "dev@example.test", "")))

	service := audit.NewService(audit.NewStore(pool), archive)

	response, err := service.VerifyChain(signedIn(t), connect.NewRequest(&governv1.VerifyChainRequest{
		CompareArchive: true,
	}))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}

	verification := response.Msg.GetVerification()
	if verification.GetIntact() {
		t.Fatal("an event that never reached the archive passed verification")
	}
	if verification.GetDetail() == "" {
		t.Error("the verification says nothing about what is wrong")
	}
}

func TestTheTrailCanBeFilteredAndRead(t *testing.T) {
	pool := dbtest.Migrated(t)
	server := newReceiver(t, pool, nil)

	post(t, server, token, batch(
		quotaRequestEvent("1", "dev@example.test", ""),
		quotaRequestEvent("2", "lead@example.test", ""),
	))

	service := audit.NewService(audit.NewStore(pool), nil)

	response, err := service.ListEvents(signedIn(t), connect.NewRequest(&governv1.ListEventsRequest{
		Actor: "lead@example.test",
	}))
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	if len(response.Msg.GetEvents()) != 1 {
		t.Fatalf("got %d events, want 1", len(response.Msg.GetEvents()))
	}

	event := response.Msg.GetEvents()[0]
	if event.GetActor().GetSubject() != "lead@example.test" {
		t.Errorf("actor: got %q", event.GetActor().GetSubject())
	}
	if event.GetHash() == "" || event.GetPrevHash() == "" {
		t.Error("the event is served without its place in the chain")
	}
	if event.GetPayloadJson() == "" {
		t.Error("the event is served without its payload")
	}
}

func TestTheTrailIsNotPublic(t *testing.T) {
	pool := dbtest.Migrated(t)
	service := audit.NewService(audit.NewStore(pool), nil)

	_, err := service.ListEvents(t.Context(), connect.NewRequest(&governv1.ListEventsRequest{}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("got %v, want unauthenticated", err)
	}
}

func newReceiver(t *testing.T, pool *pgxpool.Pool, archive *audit.FileArchive) *httptest.Server {
	t.Helper()

	store := audit.NewStore(pool)

	var sink audit.Archive
	if archive != nil {
		sink = archive
	}

	chain := audit.NewChain(store, sink)
	receiver := audit.NewReceiver(chain, token, audit.DefaultFilter(), slog.New(slog.DiscardHandler))

	mux := http.NewServeMux()
	receiver.Route(mux)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return server
}

func post(t *testing.T, server *httptest.Server, bearer string, body []byte) *http.Response {
	t.Helper()

	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/v1/audit", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}

	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("post audit batch: %v", err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })

	return response
}

func batch(items ...map[string]any) []byte {
	list := map[string]any{
		"apiVersion": "audit.k8s.io/v1",
		"kind":       "EventList",
		"items":      items,
	}

	encoded, err := json.Marshal(list)
	if err != nil {
		panic(err)
	}

	return encoded
}

func quotaRequestEvent(id, actor, impersonated string) map[string]any {
	event := map[string]any{
		"auditID": id,
		"stage":   "ResponseComplete",
		"verb":    "create",
		"user":    map[string]any{"username": actor, "groups": []string{"system:authenticated"}},
		"objectRef": map[string]any{
			"resource":  "quotarequests",
			"apiGroup":  "govern.marstack.io",
			"namespace": "payments-dev",
			"name":      "raise-quota",
		},
		"responseStatus":           map[string]any{"code": 201},
		"sourceIPs":                []string{"10.0.0.1"},
		"userAgent":                "margov",
		"requestReceivedTimestamp": time.Now().UTC().Format(time.RFC3339Nano),
	}

	if impersonated != "" {
		event["impersonatedUser"] = map[string]any{
			"username": impersonated,
			"groups":   []string{"payments-admins"},
		}
	}

	return event
}

func signedIn(t *testing.T) context.Context {
	t.Helper()

	return identity.NewContext(t.Context(), identity.Actor{Subject: "lead@example.test"})
}
