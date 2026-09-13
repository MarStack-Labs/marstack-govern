package supplychain

import (
	"fmt"
	"strings"
)

const (
	defaultRegistry  = "index.docker.io"
	defaultNamespace = "library"
	defaultTag       = "latest"
)

type Reference struct {
	Registry   string
	Repository string
	Tag        string
	Digest     string
}

func (r Reference) Pinned() bool {
	return r.Digest != ""
}

func (r Reference) String() string {
	out := r.Registry + "/" + r.Repository

	if r.Digest != "" {
		return out + "@" + r.Digest
	}

	return out + ":" + r.Tag
}

func ParseReference(image string) (Reference, error) {
	trimmed := strings.TrimSpace(image)
	if trimmed == "" {
		return Reference{}, fmt.Errorf("an image reference cannot be empty")
	}

	reference := Reference{}

	if name, digest, found := strings.Cut(trimmed, "@"); found {
		trimmed = name
		reference.Digest = digest
	}

	registry, remainder := splitRegistry(trimmed)
	reference.Registry = registry

	if repository, tag, found := lastColon(remainder); found {
		reference.Repository = repository
		reference.Tag = tag
	} else {
		reference.Repository = remainder
	}

	if reference.Repository == "" {
		return Reference{}, fmt.Errorf("%q names no repository", image)
	}

	if reference.Registry == defaultRegistry && !strings.Contains(reference.Repository, "/") {
		reference.Repository = defaultNamespace + "/" + reference.Repository
	}

	if reference.Tag == "" && reference.Digest == "" {
		reference.Tag = defaultTag
	}

	return reference, nil
}

func splitRegistry(name string) (registry, remainder string) {
	head, rest, found := strings.Cut(name, "/")
	if !found {
		return defaultRegistry, name
	}

	if !strings.ContainsAny(head, ".:") && head != "localhost" {
		return defaultRegistry, name
	}

	return head, rest
}

func lastColon(name string) (repository, tag string, found bool) {
	index := strings.LastIndex(name, ":")
	if index < 0 {
		return name, "", false
	}

	if strings.Contains(name[index+1:], "/") {
		return name, "", false
	}

	return name[:index], name[index+1:], true
}
