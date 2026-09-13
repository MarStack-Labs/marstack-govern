package delivery

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

var ErrNoCatalogue = errors.New("no template registry is configured, so there is nothing to scaffold from")

type Template struct {
	Name        string
	Version     string
	Description string
	Reference   string
	Fields      []Field
}

type Field struct {
	Name     string
	Label    string
	Kind     string
	Default  string
	Required bool
	Help     string
}

type ChartSource interface {
	Charts(ctx context.Context) ([]Chart, error)
	Available() bool
}

type Chart struct {
	Name        string
	Version     string
	Description string
	Reference   string
	Values      map[string]string
}

type Catalogue struct {
	source ChartSource
}

func NewCatalogue(source ChartSource) *Catalogue {
	return &Catalogue{source: source}
}

func (c *Catalogue) Available() bool {
	return c != nil && c.source != nil && c.source.Available()
}

func (c *Catalogue) Templates(ctx context.Context) ([]Template, error) {
	if !c.Available() {
		return nil, ErrNoCatalogue
	}

	charts, err := c.source.Charts(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the template registry: %w", err)
	}

	templates := make([]Template, 0, len(charts))
	for _, chart := range charts {
		templates = append(templates, Template{
			Name:        chart.Name,
			Version:     chart.Version,
			Description: chart.Description,
			Reference:   chart.Reference,
			Fields:      fieldsOf(chart.Values),
		})
	}

	sort.SliceStable(templates, func(i, j int) bool {
		return templates[i].Name < templates[j].Name
	})

	return templates, nil
}

func (c *Catalogue) Template(ctx context.Context, name string) (Template, error) {
	templates, err := c.Templates(ctx)
	if err != nil {
		return Template{}, err
	}

	for _, template := range templates {
		if template.Name == name {
			return template, nil
		}
	}

	return Template{}, fmt.Errorf("template %s is not in the registry", name)
}

func fieldsOf(values map[string]string) []Field {
	fields := make([]Field, 0, len(values))

	for name, value := range values {
		fields = append(fields, Field{
			Name:     name,
			Label:    humanise(name),
			Kind:     kindOf(value),
			Default:  value,
			Required: value == "",
		})
	}

	sort.SliceStable(fields, func(i, j int) bool {
		if fields[i].Required != fields[j].Required {
			return fields[i].Required
		}
		return fields[i].Name < fields[j].Name
	})

	return fields
}

func kindOf(value string) string {
	switch {
	case value == "true" || value == "false":
		return "boolean"
	case value != "" && strings.IndexFunc(value, func(r rune) bool { return r < '0' || r > '9' }) < 0:
		return "number"
	default:
		return "string"
	}
}

func humanise(name string) string {
	replaced := strings.NewReplacer(".", " ", "_", " ", "-", " ").Replace(name)
	if replaced == "" {
		return name
	}

	return strings.ToUpper(replaced[:1]) + replaced[1:]
}
