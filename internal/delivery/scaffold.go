package delivery

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

type File struct {
	Path    string
	Content string
}

type Request struct {
	Division    string
	Environment string
	Template    Template
	Name        string
	Values      map[string]string
}

func (r Request) Namespace() string {
	return r.Division + "-" + r.Environment
}

func (r Request) Branch() string {
	return fmt.Sprintf("scaffold/%s-%s", r.Environment, r.Name)
}

func (r Request) Title() string {
	return fmt.Sprintf("Add %s (%s) to %s", r.Name, r.Template.Name, r.Namespace())
}

func Validate(request Request) error {
	if !dnsName(request.Name) {
		return fmt.Errorf("%q is not a name Kubernetes will accept", request.Name)
	}
	if request.Environment == "" {
		return errors.New("name the environment the workload belongs in")
	}

	missing := []string{}
	for _, field := range request.Template.Fields {
		if !field.Required {
			continue
		}
		if strings.TrimSpace(request.Values[field.Name]) == "" {
			missing = append(missing, field.Name)
		}
	}

	if len(missing) > 0 {
		sort.Strings(missing)

		return fmt.Errorf("the template needs values for %s", strings.Join(missing, ", "))
	}

	return nil
}

func Render(request Request) ([]File, error) {
	if err := Validate(request); err != nil {
		return nil, err
	}

	values := map[string]string{}
	for _, field := range request.Template.Fields {
		values[field.Name] = field.Default
	}
	for name, value := range request.Values {
		values[name] = value
	}

	path := fmt.Sprintf("%s/%s/%s.yaml", request.Division, request.Environment, request.Name)

	return []File{{Path: path, Content: application(request, values)}}, nil
}

func application(request Request, values map[string]string) string {
	builder := &strings.Builder{}

	fmt.Fprintf(builder, "apiVersion: argoproj.io/v1alpha1\n")
	fmt.Fprintf(builder, "kind: Application\n")
	fmt.Fprintf(builder, "metadata:\n")
	fmt.Fprintf(builder, "  name: %s\n", request.Name)
	fmt.Fprintf(builder, "  namespace: argocd\n")
	fmt.Fprintf(builder, "  labels:\n")
	fmt.Fprintf(builder, "    govern.marstack.io/division: %s\n", request.Division)
	fmt.Fprintf(builder, "spec:\n")
	fmt.Fprintf(builder, "  project: %s\n", request.Division)
	fmt.Fprintf(builder, "  destination:\n")
	fmt.Fprintf(builder, "    server: https://kubernetes.default.svc\n")
	fmt.Fprintf(builder, "    namespace: %s\n", request.Namespace())
	fmt.Fprintf(builder, "  source:\n")
	fmt.Fprintf(builder, "    repoURL: %s\n", chartRepo(request.Template.Reference))
	fmt.Fprintf(builder, "    chart: %s\n", request.Template.Name)
	fmt.Fprintf(builder, "    targetRevision: %s\n", request.Template.Version)
	fmt.Fprintf(builder, "    helm:\n")
	fmt.Fprintf(builder, "      parameters:\n")

	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		fmt.Fprintf(builder, "        - name: %s\n", name)
		fmt.Fprintf(builder, "          value: %q\n", values[name])
	}

	fmt.Fprintf(builder, "  syncPolicy:\n")
	fmt.Fprintf(builder, "    automated:\n")
	fmt.Fprintf(builder, "      prune: true\n")
	fmt.Fprintf(builder, "      selfHeal: true\n")

	return builder.String()
}

func chartRepo(reference string) string {
	repo, _, found := strings.Cut(reference, ":")
	if !found {
		return reference
	}

	slash := strings.LastIndex(repo, "/")
	if slash < 0 {
		return repo
	}

	return repo[:slash]
}

func dnsName(name string) bool {
	if len(name) == 0 || len(name) > 63 {
		return false
	}

	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '-' && i > 0 && i < len(name)-1:
		default:
			return false
		}
	}

	return true
}
