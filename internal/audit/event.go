package audit

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	ActingDivisionLabel = "govern.marstack.io/division"
	emptyHashHex        = ""
)

type Event struct {
	AuditID        string            `json:"auditID"`
	EventAt        time.Time         `json:"eventAt"`
	Stage          string            `json:"stage"`
	Actor          string            `json:"actor"`
	ActorGroups    []string          `json:"actorGroups,omitempty"`
	ImpersonatedBy string            `json:"impersonatedBy,omitempty"`
	ActingDivision string            `json:"actingDivision,omitempty"`
	Verb           string            `json:"verb"`
	Resource       string            `json:"resource"`
	Subresource    string            `json:"subresource,omitempty"`
	Namespace      string            `json:"namespace,omitempty"`
	ObjectName     string            `json:"objectName,omitempty"`
	ObjectUID      string            `json:"objectUid,omitempty"`
	ResponseCode   int32             `json:"responseCode"`
	SourceIPs      []string          `json:"sourceIps,omitempty"`
	UserAgent      string            `json:"userAgent,omitempty"`
	Payload        json.RawMessage   `json:"payload"`
	Annotations    map[string]string `json:"annotations,omitempty"`
	PrevHash       []byte            `json:"-"`
	Hash           []byte            `json:"-"`
	CanonicalBytes []byte            `json:"-"`
}

func (e Event) Canonical() ([]byte, error) {
	body, err := json.Marshal(e)
	if err != nil {
		return nil, fmt.Errorf("canonicalise audit event %s: %w", e.AuditID, err)
	}

	return body, nil
}

func Digest(previous, canonical []byte) []byte {
	sum := sha256.New()
	sum.Write(previous)
	sum.Write(canonical)

	return sum.Sum(nil)
}

func (e Event) Matches(canonical []byte) bool {
	var signed Event
	if err := json.Unmarshal(canonical, &signed); err != nil {
		return false
	}

	return e.AuditID == signed.AuditID &&
		e.Actor == signed.Actor &&
		e.ImpersonatedBy == signed.ImpersonatedBy &&
		e.ActingDivision == signed.ActingDivision &&
		e.Verb == signed.Verb &&
		e.Resource == signed.Resource &&
		e.Subresource == signed.Subresource &&
		e.Namespace == signed.Namespace &&
		e.ObjectName == signed.ObjectName &&
		e.ObjectUID == signed.ObjectUID &&
		e.ResponseCode == signed.ResponseCode &&
		e.EventAt.UTC().Truncate(time.Microsecond).Equal(signed.EventAt.UTC().Truncate(time.Microsecond))
}

func (e Event) Describe() string {
	target := e.Resource
	if e.Namespace != "" {
		target = e.Namespace + "/" + target
	}
	if e.ObjectName != "" {
		target += "/" + e.ObjectName
	}

	return strings.TrimSpace(fmt.Sprintf("%s %s %s", e.Actor, e.Verb, target))
}
