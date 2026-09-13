package policy

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var ErrKyvernoAbsent = errors.New("kyverno is not installed, so no policy is being evaluated")

var (
	ClusterPolicyGVK = schema.GroupVersionKind{
		Group: "kyverno.io", Version: "v1", Kind: "ClusterPolicy",
	}
	PolicyGVK = schema.GroupVersionKind{
		Group: "kyverno.io", Version: "v1", Kind: "Policy",
	}
	PolicyReportGVK = schema.GroupVersionKind{
		Group: "wgpolicyk8s.io", Version: "v1alpha2", Kind: "PolicyReport",
	}
	ClusterPolicyReportGVK = schema.GroupVersionKind{
		Group: "wgpolicyk8s.io", Version: "v1alpha2", Kind: "ClusterPolicyReport",
	}
)

type Guardrail struct {
	Name          string
	Kind          string
	ClusterScoped bool
	Enforcement   string
	Description   string
	Categories    []string
	Severity      string
	Rules         []string
	Ready         bool
}

type Finding struct {
	Policy       string
	Rule         string
	Namespace    string
	ResourceKind string
	ResourceName string
	Severity     string
	Result       string
	Message      string
	ObservedAt   time.Time
}

type Reader struct {
	client client.Client
}

func NewReader(c client.Client) *Reader {
	return &Reader{client: c}
}

func (r *Reader) Available(gvk schema.GroupVersionKind) bool {
	mapper := r.client.RESTMapper()
	if mapper == nil {
		return true
	}

	_, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)

	return err == nil
}

func (r *Reader) Policies(ctx context.Context) ([]Guardrail, error) {
	if !r.Available(ClusterPolicyGVK) {
		return nil, ErrKyvernoAbsent
	}

	guardrails := []Guardrail{}

	for _, gvk := range []schema.GroupVersionKind{ClusterPolicyGVK, PolicyGVK} {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(listKind(gvk))

		if err := r.client.List(ctx, list); err != nil {
			if absent(err) {
				return nil, ErrKyvernoAbsent
			}
			return nil, fmt.Errorf("list %s: %w", gvk.Kind, err)
		}

		for i := range list.Items {
			guardrails = append(guardrails, guardrailFrom(&list.Items[i], gvk.Kind == "ClusterPolicy"))
		}
	}

	sort.Slice(guardrails, func(i, j int) bool { return guardrails[i].Name < guardrails[j].Name })

	return guardrails, nil
}

func (r *Reader) Findings(ctx context.Context, namespaces []string) ([]Finding, error) {
	if !r.Available(PolicyReportGVK) {
		return nil, ErrKyvernoAbsent
	}

	findings := []Finding{}

	lists := []struct {
		gvk        schema.GroupVersionKind
		namespaced bool
	}{
		{PolicyReportGVK, true},
		{ClusterPolicyReportGVK, false},
	}

	for _, source := range lists {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(listKind(source.gvk))

		options := []client.ListOption{}
		if source.namespaced && len(namespaces) == 1 {
			options = append(options, client.InNamespace(namespaces[0]))
		}

		if err := r.client.List(ctx, list, options...); err != nil {
			if absent(err) {
				return nil, ErrKyvernoAbsent
			}
			return nil, fmt.Errorf("list %s: %w", source.gvk.Kind, err)
		}

		for i := range list.Items {
			report := &list.Items[i]
			if source.namespaced && !within(report.GetNamespace(), namespaces) {
				continue
			}

			findings = append(findings, findingsFrom(report)...)
		}
	}

	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Namespace != findings[j].Namespace {
			return findings[i].Namespace < findings[j].Namespace
		}
		return findings[i].Policy < findings[j].Policy
	})

	return findings, nil
}

func guardrailFrom(object *unstructured.Unstructured, clusterScoped bool) Guardrail {
	guardrail := Guardrail{
		Name:          object.GetName(),
		Kind:          object.GetKind(),
		ClusterScoped: clusterScoped,
		Enforcement:   "audit",
		Description:   object.GetAnnotations()["policies.kyverno.io/description"],
		Severity:      object.GetAnnotations()["policies.kyverno.io/severity"],
	}

	if category := object.GetAnnotations()["policies.kyverno.io/category"]; category != "" {
		for _, item := range strings.Split(category, ",") {
			if trimmed := strings.TrimSpace(item); trimmed != "" {
				guardrail.Categories = append(guardrail.Categories, trimmed)
			}
		}
	}

	if action, found, _ := unstructured.NestedString(object.Object, "spec", "validationFailureAction"); found {
		guardrail.Enforcement = strings.ToLower(action)
	}

	rules, found, _ := unstructured.NestedSlice(object.Object, "spec", "rules")
	if found {
		for _, item := range rules {
			rule, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if name, ok := rule["name"].(string); ok {
				guardrail.Rules = append(guardrail.Rules, name)
			}
		}
	}

	conditions, found, _ := unstructured.NestedSlice(object.Object, "status", "conditions")
	if found {
		for _, item := range conditions {
			condition, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if condition["type"] == "Ready" && condition["status"] == "True" {
				guardrail.Ready = true
			}
		}
	}

	return guardrail
}

func findingsFrom(report *unstructured.Unstructured) []Finding {
	results, found, _ := unstructured.NestedSlice(report.Object, "results")
	if !found {
		return nil
	}

	observed := report.GetCreationTimestamp().Time
	if observed.IsZero() {
		observed = time.Now()
	}

	findings := []Finding{}

	for _, item := range results {
		result, ok := item.(map[string]any)
		if !ok {
			continue
		}

		outcome, _ := result["result"].(string)
		if outcome == "pass" || outcome == "skip" {
			continue
		}

		finding := Finding{
			Policy:     stringField(result, "policy"),
			Rule:       stringField(result, "rule"),
			Namespace:  report.GetNamespace(),
			Severity:   normalSeverity(stringField(result, "severity")),
			Result:     normalResult(outcome),
			Message:    stringField(result, "message"),
			ObservedAt: observed,
		}

		if resources, ok := result["resources"].([]any); ok && len(resources) > 0 {
			if resource, ok := resources[0].(map[string]any); ok {
				finding.ResourceKind = stringField(resource, "kind")
				finding.ResourceName = stringField(resource, "name")
				if namespace := stringField(resource, "namespace"); namespace != "" {
					finding.Namespace = namespace
				}
			}
		}

		if finding.Policy == "" || finding.ResourceName == "" {
			continue
		}

		findings = append(findings, finding)
	}

	return findings
}

func stringField(source map[string]any, key string) string {
	value, _ := source[key].(string)

	return value
}

func normalSeverity(value string) string {
	switch strings.ToLower(value) {
	case "critical":
		return "critical"
	case "high":
		return "high"
	case "low":
		return "low"
	default:
		return "medium"
	}
}

func normalResult(value string) string {
	switch strings.ToLower(value) {
	case "warn":
		return "warn"
	case "error":
		return "error"
	default:
		return "fail"
	}
}

func listKind(gvk schema.GroupVersionKind) schema.GroupVersionKind {
	gvk.Kind += "List"

	return gvk
}

func within(namespace string, namespaces []string) bool {
	if len(namespaces) == 0 {
		return true
	}

	for _, candidate := range namespaces {
		if candidate == namespace {
			return true
		}
	}

	return false
}

func absent(err error) bool {
	if meta.IsNoMatchError(err) {
		return true
	}

	if runtime.IsNotRegisteredError(err) {
		return true
	}

	return apierrors.IsNotFound(err) &&
		strings.Contains(err.Error(), "the server could not find the requested resource")
}
