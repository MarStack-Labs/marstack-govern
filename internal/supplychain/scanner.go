package supplychain

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var ErrScannerAbsent = errors.New("no vulnerability scanner is installed, so nothing is known about CVEs")

var (
	VulnerabilityReportGVK = schema.GroupVersionKind{
		Group: "aquasecurity.github.io", Version: "v1alpha1", Kind: "VulnerabilityReport",
	}
	SbomReportGVK = schema.GroupVersionKind{
		Group: "aquasecurity.github.io", Version: "v1alpha1", Kind: "SbomReport",
	}
)

type Vulnerabilities struct {
	Critical  int32
	High      int32
	Medium    int32
	Low       int32
	ScannedAt time.Time
	Found     bool
}

type Scanner struct {
	client client.Client
}

func NewScanner(c client.Client) *Scanner {
	return &Scanner{client: c}
}

func (s *Scanner) Available() bool {
	mapper := s.client.RESTMapper()
	if mapper == nil {
		return true
	}

	_, err := mapper.RESTMapping(VulnerabilityReportGVK.GroupKind(), VulnerabilityReportGVK.Version)

	return err == nil
}

func (s *Scanner) Vulnerabilities(ctx context.Context, namespace, digest string) (Vulnerabilities, error) {
	if !s.Available() {
		return Vulnerabilities{}, ErrScannerAbsent
	}

	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(listKind(VulnerabilityReportGVK))

	if err := s.client.List(ctx, list, client.InNamespace(namespace)); err != nil {
		return Vulnerabilities{}, fmt.Errorf("list vulnerability reports in %s: %w", namespace, err)
	}

	total := Vulnerabilities{}

	for i := range list.Items {
		report := &list.Items[i]
		if digest != "" && !reportsOn(report, digest) {
			continue
		}

		counts, found, _ := unstructured.NestedMap(report.Object, "report", "summary")
		if !found {
			continue
		}

		total.Found = true
		total.Critical += count(counts, "criticalCount")
		total.High += count(counts, "highCount")
		total.Medium += count(counts, "mediumCount")
		total.Low += count(counts, "lowCount")

		if stamp, found, _ := unstructured.NestedString(report.Object, "report", "updateTimestamp"); found {
			if parsed, err := time.Parse(time.RFC3339, stamp); err == nil && parsed.After(total.ScannedAt) {
				total.ScannedAt = parsed
			}
		}
	}

	return total, nil
}

func (s *Scanner) HasSBOM(ctx context.Context, namespace, digest string) (bool, error) {
	mapper := s.client.RESTMapper()
	if mapper != nil {
		if _, err := mapper.RESTMapping(SbomReportGVK.GroupKind(), SbomReportGVK.Version); err != nil {
			return false, nil
		}
	}

	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(listKind(SbomReportGVK))

	if err := s.client.List(ctx, list, client.InNamespace(namespace)); err != nil {
		return false, fmt.Errorf("list sbom reports in %s: %w", namespace, err)
	}

	for i := range list.Items {
		if digest == "" || reportsOn(&list.Items[i], digest) {
			return true, nil
		}
	}

	return false, nil
}

func reportsOn(report *unstructured.Unstructured, digest string) bool {
	if value, found, _ := unstructured.NestedString(report.Object, "report", "artifact", "digest"); found {
		if value == digest {
			return true
		}
	}

	for key, value := range report.GetLabels() {
		if strings.Contains(key, "resource") && strings.Contains(digest, value) {
			return true
		}
	}

	return false
}

func count(summary map[string]any, key string) int32 {
	var raw int64

	switch value := summary[key].(type) {
	case int64:
		raw = value
	case float64:
		raw = int64(value)
	default:
		return 0
	}

	if raw > math.MaxInt32 {
		raw = math.MaxInt32
	}
	if raw < 0 {
		raw = 0
	}

	return int32(raw)
}

func listKind(gvk schema.GroupVersionKind) schema.GroupVersionKind {
	gvk.Kind += "List"

	return gvk
}
