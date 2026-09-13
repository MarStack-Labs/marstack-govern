package delivery

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	governv1alpha1 "github.com/marstack-labs/marstack-govern/api/v1alpha1"
	governv1 "github.com/marstack-labs/marstack-govern/gen/marstack/govern/v1"
	"github.com/marstack-labs/marstack-govern/internal/identity"
)

type Admission interface {
	Admits(ctx context.Context, namespace string, manifest string) (bool, []Finding, error)
}

type Finding struct {
	Severity string
	Check    string
	Message  string
	Field    string
}

type Service struct {
	reader    client.Client
	catalogue *Catalogue
	forge     Forge
	admission Admission
	repoOf    func(division string) string
}

func NewService(reader client.Client, catalogue *Catalogue) *Service {
	return &Service{reader: reader, catalogue: catalogue}
}

func (s *Service) WithForge(forge Forge, repoOf func(string) string) *Service {
	s.forge = forge
	s.repoOf = repoOf

	return s
}

func (s *Service) WithAdmission(admission Admission) *Service {
	s.admission = admission

	return s
}

func (s *Service) ListTemplates(
	ctx context.Context,
	_ *connect.Request[governv1.ListTemplatesRequest],
) (*connect.Response[governv1.ListTemplatesResponse], error) {
	if _, err := s.caller(ctx); err != nil {
		return nil, err
	}

	templates, err := s.catalogue.Templates(ctx)
	if errors.Is(err, ErrNoCatalogue) {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	response := &governv1.ListTemplatesResponse{
		Templates: make([]*governv1.Template, 0, len(templates)),
		Freshness: &governv1.Freshness{ObservedAt: timestamppb.New(time.Now())},
	}
	for _, template := range templates {
		response.Templates = append(response.Templates, protoTemplate(template))
	}

	return connect.NewResponse(response), nil
}

func (s *Service) Scaffold(
	ctx context.Context,
	req *connect.Request[governv1.ScaffoldRequest],
) (*connect.Response[governv1.ScaffoldResponse], error) {
	if _, err := s.caller(ctx); err != nil {
		return nil, err
	}

	request, err := s.build(ctx, req.Msg.GetDivision(), req.Msg.GetEnvironment(),
		req.Msg.GetTemplate(), req.Msg.GetName(), req.Msg.GetValues())
	if err != nil {
		return nil, err
	}

	files, err := Render(request)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	response := &governv1.ScaffoldResponse{
		Files:    make([]*governv1.Scaffolded, 0, len(files)),
		Admitted: true,
	}
	for _, file := range files {
		response.Files = append(response.Files, &governv1.Scaffolded{
			Path:    file.Path,
			Content: file.Content,
		})
	}

	if s.admission != nil {
		admitted, findings, err := s.admission.Admits(ctx, request.Namespace(), files[0].Content)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}

		response.Admitted = admitted
		for _, finding := range findings {
			response.Findings = append(response.Findings, &governv1.PreflightFinding{
				Severity: finding.Severity,
				Check:    finding.Check,
				Message:  finding.Message,
				Field:    finding.Field,
			})
		}
	} else {
		response.Admitted = false
		response.Findings = append(response.Findings, &governv1.PreflightFinding{
			Severity: "warn",
			Check:    "preflight",
			Message:  "no admission chain is attached, so this manifest has not been checked against one",
		})
	}

	return connect.NewResponse(response), nil
}

func (s *Service) OpenChange(
	ctx context.Context,
	req *connect.Request[governv1.OpenChangeRequest],
) (*connect.Response[governv1.OpenChangeResponse], error) {
	actor, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}

	if s.forge == nil || !s.forge.Available() {
		return nil, connect.NewError(connect.CodeUnavailable, ErrNoForge)
	}

	reason := strings.TrimSpace(req.Msg.GetReason())
	if len(reason) < 10 {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("say why this is being added, in at least ten characters"))
	}

	request, err := s.build(ctx, req.Msg.GetDivision(), req.Msg.GetEnvironment(),
		req.Msg.GetTemplate(), req.Msg.GetName(), req.Msg.GetValues())
	if err != nil {
		return nil, err
	}

	files, err := Render(request)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	if s.admission != nil {
		admitted, findings, err := s.admission.Admits(ctx, request.Namespace(), files[0].Content)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		if !admitted {
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				fmt.Errorf("the cluster would reject this manifest: %s", summarise(findings)))
		}
	}

	repository := s.repository(request.Division)
	if repository == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("division %s has no gitops repository configured", request.Division))
	}

	body := fmt.Sprintf("%s\n\nOpened from marstack-govern by %s.\n\nTemplate: %s %s\nNamespace: %s\n",
		reason, actor.Label(), request.Template.Name, request.Template.Version, request.Namespace())

	change, err := s.forge.Open(ctx, repository, request.Branch(), request.Title(), body, files, actor.Label())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&governv1.OpenChangeResponse{Change: protoChange(change)}), nil
}

func (s *Service) ListChanges(
	ctx context.Context,
	req *connect.Request[governv1.ListChangesRequest],
) (*connect.Response[governv1.ListChangesResponse], error) {
	if _, err := s.caller(ctx); err != nil {
		return nil, err
	}

	if s.forge == nil || !s.forge.Available() {
		return nil, connect.NewError(connect.CodeUnavailable, ErrNoForge)
	}

	division, err := s.division(ctx, req.Msg.GetDivision())
	if err != nil {
		return nil, err
	}

	repository := s.repository(division.Name)
	if repository == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("division %s has no gitops repository configured", division.Name))
	}

	changes, err := s.forge.Changes(ctx, repository)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	response := &governv1.ListChangesResponse{
		Changes:   make([]*governv1.MergeRequest, 0, len(changes)),
		Freshness: &governv1.Freshness{ObservedAt: timestamppb.New(time.Now())},
	}
	for _, change := range changes {
		response.Changes = append(response.Changes, protoChange(change))
	}

	return connect.NewResponse(response), nil
}

func (s *Service) build(
	ctx context.Context,
	divisionName, environment, templateName, name string,
	values map[string]string,
) (Request, error) {
	division, err := s.division(ctx, divisionName)
	if err != nil {
		return Request{}, err
	}

	if !slices.Contains(division.Spec.Environments, environment) {
		return Request{}, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("division %s has no %s environment", division.Name, environment))
	}

	template, err := s.catalogue.Template(ctx, templateName)
	if errors.Is(err, ErrNoCatalogue) {
		return Request{}, connect.NewError(connect.CodeUnavailable, err)
	}
	if err != nil {
		return Request{}, connect.NewError(connect.CodeNotFound, err)
	}

	return Request{
		Division:    division.Name,
		Environment: environment,
		Template:    template,
		Name:        name,
		Values:      values,
	}, nil
}

func (s *Service) repository(division string) string {
	if s.repoOf == nil {
		return ""
	}

	return s.repoOf(division)
}

func (s *Service) division(ctx context.Context, name string) (*governv1alpha1.Division, error) {
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("name the division explicitly"))
	}

	division := &governv1alpha1.Division{}
	if err := s.reader.Get(ctx, types.NamespacedName{Name: name}, division); err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("division %s not found", name))
	}

	return division, nil
}

func (s *Service) caller(ctx context.Context) (identity.Actor, error) {
	actor, ok := identity.FromContext(ctx)
	if !ok {
		return identity.Actor{}, connect.NewError(connect.CodeUnauthenticated, errors.New("sign in at /auth/login"))
	}

	return actor, nil
}

func summarise(findings []Finding) string {
	messages := make([]string, 0, len(findings))
	for _, finding := range findings {
		if finding.Severity == "block" {
			messages = append(messages, finding.Check+": "+finding.Message)
		}
	}

	if len(messages) == 0 {
		return "no reason was given"
	}

	return strings.Join(messages, "; ")
}

func protoTemplate(template Template) *governv1.Template {
	out := &governv1.Template{
		Name:        template.Name,
		Version:     template.Version,
		Description: template.Description,
		Reference:   template.Reference,
		Fields:      make([]*governv1.TemplateField, 0, len(template.Fields)),
	}

	for _, field := range template.Fields {
		out.Fields = append(out.Fields, &governv1.TemplateField{
			Name:         field.Name,
			Label:        field.Label,
			Kind:         field.Kind,
			DefaultValue: field.Default,
			Required:     field.Required,
			Help:         field.Help,
		})
	}

	return out
}

func protoChange(change Change) *governv1.MergeRequest {
	return &governv1.MergeRequest{
		Id:       change.ID,
		Title:    change.Title,
		Branch:   change.Branch,
		Url:      change.URL,
		Author:   change.Author,
		State:    change.State,
		OpenedAt: timestamppb.New(change.OpenedAt),
	}
}
