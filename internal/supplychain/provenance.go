package supplychain

import (
	"context"
	"errors"
	"time"

	governv1 "github.com/marstack-labs/marstack-govern/gen/marstack/govern/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Provenance struct {
	registry *Registry
	scanner  *Scanner
}

func NewProvenance(registry *Registry, scanner *Scanner) *Provenance {
	return &Provenance{registry: registry, scanner: scanner}
}

func (p *Provenance) Of(ctx context.Context, namespace, image string) (*governv1.Provenance, error) {
	reference, err := ParseReference(image)
	if err != nil {
		return nil, err
	}

	provenance := &governv1.Provenance{
		Registry:   reference.Registry,
		Repository: reference.Repository,
		Tag:        reference.Tag,
		TagMutable: !reference.Pinned() && !immutableTag(reference.Tag),
	}

	if reference.Digest != "" {
		provenance.ImageDigest = reference.Digest
	}

	if p.registry != nil {
		facts, err := p.registry.Inspect(ctx, reference)
		switch {
		case err == nil:
			provenance.ImageDigest = facts.Digest
			provenance.SourceRepo = facts.Source
			provenance.SourceRevision = facts.Revision
			provenance.Signed = facts.Signed
			if !facts.Created.IsZero() {
				provenance.BaseImageAgeDays = int32(time.Since(facts.Created).Hours() / 24)
			}
		case errors.Is(err, ErrNeedsCredentials), errors.Is(err, ErrNotFound):
			provenance.SignatureIssuer = err.Error()
		default:
			return nil, err
		}
	}

	if p.scanner != nil {
		vulnerabilities, err := p.scanner.Vulnerabilities(ctx, namespace, provenance.GetImageDigest())
		if err == nil && vulnerabilities.Found {
			provenance.Vulnerabilities = &governv1.Vulnerabilities{
				Critical: vulnerabilities.Critical,
				High:     vulnerabilities.High,
				Medium:   vulnerabilities.Medium,
				Low:      vulnerabilities.Low,
			}
			if !vulnerabilities.ScannedAt.IsZero() {
				provenance.ScannedAt = timestamppb.New(vulnerabilities.ScannedAt)
			}
		}

		if sbom, err := p.scanner.HasSBOM(ctx, namespace, provenance.GetImageDigest()); err == nil {
			provenance.SbomPresent = sbom
		}
	}

	return provenance, nil
}

func immutableTag(tag string) bool {
	switch tag {
	case "", "latest", "main", "master", "stable", "edge", "nightly":
		return false
	default:
		return true
	}
}
