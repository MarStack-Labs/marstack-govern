package audit

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/types/known/timestamppb"

	governv1 "github.com/marstack-labs/marstack-govern/gen/marstack/govern/v1"
	"github.com/marstack-labs/marstack-govern/internal/identity"
)

type Service struct {
	store   *Store
	archive *FileArchive
}

func NewService(store *Store, archive *FileArchive) *Service {
	return &Service{store: store, archive: archive}
}

func (s *Service) ListEvents(
	ctx context.Context,
	req *connect.Request[governv1.ListEventsRequest],
) (*connect.Response[governv1.ListEventsResponse], error) {
	if _, err := s.caller(ctx); err != nil {
		return nil, err
	}

	query := Query{
		Division:  req.Msg.GetDivision(),
		Actor:     req.Msg.GetActor(),
		Verb:      req.Msg.GetVerb(),
		Resource:  req.Msg.GetResource(),
		ObjectUID: req.Msg.GetObjectUid(),
	}

	if page := req.Msg.GetPage(); page != nil {
		query.Limit = page.GetSize()
	}
	if since := req.Msg.GetSince(); since != nil {
		at := since.AsTime()
		query.Since = &at
	}
	if until := req.Msg.GetUntil(); until != nil {
		at := until.AsTime()
		query.Until = &at
	}

	records, err := s.store.List(ctx, query)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	response := &governv1.ListEventsResponse{
		Events:    make([]*governv1.AuditEvent, 0, len(records)),
		Page:      &governv1.PageInfo{},
		Freshness: &governv1.Freshness{ObservedAt: timestamppb.New(time.Now())},
	}
	for _, record := range records {
		response.Events = append(response.Events, protoEvent(record))
	}

	return connect.NewResponse(response), nil
}

func (s *Service) GetEvent(
	ctx context.Context,
	req *connect.Request[governv1.GetEventRequest],
) (*connect.Response[governv1.GetEventResponse], error) {
	if _, err := s.caller(ctx); err != nil {
		return nil, err
	}

	record, err := s.store.Get(ctx, req.Msg.GetSeq())
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("audit event %d not found", req.Msg.GetSeq()))
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&governv1.GetEventResponse{Event: protoEvent(record)}), nil
}

func (s *Service) VerifyChain(
	ctx context.Context,
	req *connect.Request[governv1.VerifyChainRequest],
) (*connect.Response[governv1.VerifyChainResponse], error) {
	if _, err := s.caller(ctx); err != nil {
		return nil, err
	}

	from := req.Msg.GetFromSeq()
	if from <= 0 {
		from = 1
	}

	records, start, err := s.store.Range(ctx, from, req.Msg.GetToSeq())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	events := make([]Event, 0, len(records))
	for _, record := range records {
		events = append(events, record.Event)
	}

	verification := Verify(events, start)

	if len(records) > 0 {
		verification.FirstSeq = records[0].Seq
		verification.LastSeq = records[len(records)-1].Seq
		if !verification.Intact && verification.BrokenAtSeq < int64(len(records)) {
			verification.BrokenAtSeq = records[verification.BrokenAtSeq].Seq
		}
	}

	if req.Msg.GetCompareArchive() && s.archive != nil {
		if err := s.compareArchive(records, &verification); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}

	return connect.NewResponse(&governv1.VerifyChainResponse{
		Verification: &governv1.ChainVerification{
			Intact:          verification.Intact,
			CheckedEvents:   verification.Checked,
			FirstSeq:        verification.FirstSeq,
			LastSeq:         verification.LastSeq,
			BrokenAtSeq:     verification.BrokenAtSeq,
			Detail:          verification.Detail,
			ArchiveCompared: verification.ArchiveChecked,
			VerifiedAt:      timestamppb.New(time.Now()),
		},
	}), nil
}

func (s *Service) compareArchive(records []Record, verification *Verification) error {
	hashes, err := s.archive.Hashes()
	if err != nil {
		return err
	}

	verification.ArchiveChecked = true

	for _, record := range records {
		archivedHash, found := hashes[record.AuditID]
		if !found {
			verification.Intact = false
			verification.BrokenAtSeq = record.Seq
			verification.Detail = fmt.Sprintf("event %s is in the database but not in the archive", record.AuditID)

			return nil
		}

		if archivedHash != hex.EncodeToString(record.Hash) {
			verification.Intact = false
			verification.BrokenAtSeq = record.Seq
			verification.Detail = fmt.Sprintf("event %s does not match the archived copy", record.AuditID)

			return nil
		}
	}

	return nil
}

func (s *Service) caller(ctx context.Context) (identity.Actor, error) {
	actor, ok := identity.FromContext(ctx)
	if !ok {
		return identity.Actor{}, connect.NewError(connect.CodeUnauthenticated, errors.New("sign in at /auth/login"))
	}

	return actor, nil
}

func protoEvent(record Record) *governv1.AuditEvent {
	return &governv1.AuditEvent{
		Seq:            record.Seq,
		AuditId:        record.AuditID,
		EventAt:        timestamppb.New(record.EventAt),
		Actor:          &governv1.Actor{Subject: record.Actor, Groups: record.ActorGroups, ActingDivision: record.ActingDivision},
		ImpersonatedBy: record.ImpersonatedBy,
		Verb:           record.Verb,
		Resource:       record.Resource,
		Subresource:    record.Subresource,
		Namespace:      record.Namespace,
		ObjectName:     record.ObjectName,
		ObjectUid:      record.ObjectUID,
		ResponseCode:   record.ResponseCode,
		SourceIps:      record.SourceIPs,
		UserAgent:      record.UserAgent,
		PayloadJson:    string(record.Payload),
		PrevHash:       hex.EncodeToString(record.PrevHash),
		Hash:           hex.EncodeToString(record.Hash),
	}
}
