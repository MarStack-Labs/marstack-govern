package audit

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const maxBody = 32 << 20

type Filter struct {
	Groups    []string
	Resources []string
}

func (f Filter) Allows(group, resource string) bool {
	if len(f.Groups) == 0 && len(f.Resources) == 0 {
		return true
	}

	for _, allowed := range f.Groups {
		if allowed == group {
			return true
		}
	}

	for _, allowed := range f.Resources {
		if allowed == resource {
			return true
		}
	}

	return false
}

func DefaultFilter() Filter {
	return Filter{
		Groups: []string{"govern.marstack.io", "rbac.authorization.k8s.io"},
		Resources: []string{
			"namespaces",
			"resourcequotas",
			"limitranges",
			"networkpolicies",
			"serviceaccounts",
			"secrets",
		},
	}
}

type Receiver struct {
	chain  *Chain
	token  string
	filter Filter
	logger *slog.Logger
}

func NewReceiver(chain *Chain, token string, filter Filter, logger *slog.Logger) *Receiver {
	if logger == nil {
		logger = slog.Default()
	}

	return &Receiver{chain: chain, token: token, filter: filter, logger: logger}
}

func (r *Receiver) Route(mux *http.ServeMux) {
	mux.Handle("POST /v1/audit", r)
}

func (r *Receiver) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	if !r.authorised(request) {
		http.Error(w, "the audit webhook needs its bearer token", http.StatusUnauthorized)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, request.Body, maxBody))
	if err != nil {
		http.Error(w, "cannot read the audit batch", http.StatusBadRequest)
		return
	}

	var list eventList
	if err := json.Unmarshal(body, &list); err != nil {
		http.Error(w, "the audit batch is not an EventList", http.StatusBadRequest)
		return
	}

	events := make([]Event, 0, len(list.Items))
	for _, item := range list.Items {
		event, ok := r.convert(item)
		if !ok {
			continue
		}
		events = append(events, event)
	}

	written, err := r.chain.Append(request.Context(), events)
	if err != nil {
		r.logger.Error("append audit events", "error", err)
		http.Error(w, "cannot record the audit batch", http.StatusServiceUnavailable)
		return
	}

	if written > 0 {
		r.logger.Debug("recorded audit events", "count", written)
	}

	w.WriteHeader(http.StatusOK)
}

func (r *Receiver) authorised(request *http.Request) bool {
	if r.token == "" {
		return false
	}

	header := request.Header.Get("Authorization")
	presented, found := strings.CutPrefix(header, "Bearer ")
	if !found {
		return false
	}

	return subtle.ConstantTimeCompare([]byte(presented), []byte(r.token)) == 1
}

func (r *Receiver) convert(item auditEvent) (Event, bool) {
	if item.Stage != "ResponseComplete" && item.Stage != "Panic" {
		return Event{}, false
	}

	if item.ObjectRef == nil {
		return Event{}, false
	}

	if !r.filter.Allows(item.ObjectRef.APIGroup, item.ObjectRef.Resource) {
		return Event{}, false
	}

	payload, err := json.Marshal(item)
	if err != nil {
		return Event{}, false
	}

	eventAt := item.RequestReceivedTimestamp
	if eventAt.IsZero() {
		eventAt = time.Now().UTC()
	}

	event := Event{
		AuditID:        item.AuditID,
		EventAt:        eventAt.UTC(),
		Stage:          item.Stage,
		Actor:          item.User.Username,
		ActorGroups:    item.User.Groups,
		Verb:           item.Verb,
		Resource:       qualified(item.ObjectRef.APIGroup, item.ObjectRef.Resource),
		Subresource:    item.ObjectRef.Subresource,
		Namespace:      item.ObjectRef.Namespace,
		ObjectName:     item.ObjectRef.Name,
		ObjectUID:      item.ObjectRef.UID,
		SourceIPs:      item.SourceIPs,
		UserAgent:      item.UserAgent,
		Payload:        payload,
		Annotations:    item.Annotations,
		ActingDivision: item.Annotations[ActingDivisionLabel],
	}

	if item.ImpersonatedUser != nil {
		event.ImpersonatedBy = event.Actor
		event.Actor = item.ImpersonatedUser.Username
		event.ActorGroups = item.ImpersonatedUser.Groups
	}

	if item.ResponseStatus != nil {
		event.ResponseCode = item.ResponseStatus.Code
	}

	if event.AuditID == "" || event.Actor == "" {
		return Event{}, false
	}

	return event, true
}

func qualified(group, resource string) string {
	if group == "" {
		return resource
	}

	return fmt.Sprintf("%s.%s", resource, group)
}

type eventList struct {
	Items []auditEvent `json:"items"`
}

type auditEvent struct {
	AuditID                  string            `json:"auditID"`
	Stage                    string            `json:"stage"`
	Verb                     string            `json:"verb"`
	User                     auditUser         `json:"user"`
	ImpersonatedUser         *auditUser        `json:"impersonatedUser,omitempty"`
	SourceIPs                []string          `json:"sourceIPs,omitempty"`
	UserAgent                string            `json:"userAgent,omitempty"`
	ObjectRef                *objectReference  `json:"objectRef,omitempty"`
	ResponseStatus           *responseStatus   `json:"responseStatus,omitempty"`
	RequestReceivedTimestamp time.Time         `json:"requestReceivedTimestamp"`
	Annotations              map[string]string `json:"annotations,omitempty"`
}

type auditUser struct {
	Username string   `json:"username"`
	Groups   []string `json:"groups,omitempty"`
}

type objectReference struct {
	Resource    string `json:"resource"`
	Namespace   string `json:"namespace,omitempty"`
	Name        string `json:"name,omitempty"`
	UID         string `json:"uid,omitempty"`
	APIGroup    string `json:"apiGroup,omitempty"`
	APIVersion  string `json:"apiVersion,omitempty"`
	Subresource string `json:"subresource,omitempty"`
}

type responseStatus struct {
	Code int32 `json:"code"`
}
