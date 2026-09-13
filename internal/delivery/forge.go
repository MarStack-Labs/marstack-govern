package delivery

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var ErrNoForge = errors.New("no git forge is configured, so a change can be previewed but not opened")

type Change struct {
	ID       string
	Title    string
	Branch   string
	URL      string
	Author   string
	State    string
	OpenedAt time.Time
}

type Forge interface {
	Open(ctx context.Context, repository, branch, title, body string, files []File, author string) (Change, error)
	Changes(ctx context.Context, repository string) ([]Change, error)
	Available() bool
}

type GitHubConfig struct {
	BaseURL string
	Token   string
	Client  *http.Client
}

type GitHub struct {
	baseURL string
	token   string
	client  *http.Client
}

func NewGitHub(cfg GitHubConfig) *GitHub {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = "https://api.github.com"
	}

	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}

	return &GitHub{baseURL: base, token: cfg.Token, client: client}
}

func (g *GitHub) Available() bool {
	return g != nil && g.token != ""
}

func (g *GitHub) Open(
	ctx context.Context,
	repository, branch, title, body string,
	files []File,
	author string,
) (Change, error) {
	if !g.Available() {
		return Change{}, ErrNoForge
	}

	base, err := g.defaultBranch(ctx, repository)
	if err != nil {
		return Change{}, err
	}

	head, err := g.headSHA(ctx, repository, base)
	if err != nil {
		return Change{}, err
	}

	if err := g.branch(ctx, repository, branch, head); err != nil {
		return Change{}, err
	}

	for _, file := range files {
		if err := g.commit(ctx, repository, branch, file, title); err != nil {
			return Change{}, err
		}
	}

	return g.pull(ctx, repository, branch, base, title, body, author)
}

func (g *GitHub) Changes(ctx context.Context, repository string) ([]Change, error) {
	if !g.Available() {
		return nil, ErrNoForge
	}

	var pulls []struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		HTMLURL string `json:"html_url"`
		State   string `json:"state"`
		Head    struct {
			Ref string `json:"ref"`
		} `json:"head"`
		User struct {
			Login string `json:"login"`
		} `json:"user"`
		CreatedAt time.Time `json:"created_at"`
	}

	if err := g.call(ctx, http.MethodGet,
		fmt.Sprintf("/repos/%s/pulls?state=open&per_page=50", repository), nil, &pulls); err != nil {
		return nil, err
	}

	changes := make([]Change, 0, len(pulls))
	for _, pull := range pulls {
		changes = append(changes, Change{
			ID:       fmt.Sprintf("%d", pull.Number),
			Title:    pull.Title,
			Branch:   pull.Head.Ref,
			URL:      pull.HTMLURL,
			Author:   pull.User.Login,
			State:    pull.State,
			OpenedAt: pull.CreatedAt,
		})
	}

	return changes, nil
}

func (g *GitHub) defaultBranch(ctx context.Context, repository string) (string, error) {
	var repo struct {
		DefaultBranch string `json:"default_branch"`
	}

	if err := g.call(ctx, http.MethodGet, "/repos/"+repository, nil, &repo); err != nil {
		return "", err
	}
	if repo.DefaultBranch == "" {
		return "", fmt.Errorf("%s reports no default branch", repository)
	}

	return repo.DefaultBranch, nil
}

func (g *GitHub) headSHA(ctx context.Context, repository, branch string) (string, error) {
	var reference struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}

	endpoint := fmt.Sprintf("/repos/%s/git/ref/heads/%s", repository, url.PathEscape(branch))
	if err := g.call(ctx, http.MethodGet, endpoint, nil, &reference); err != nil {
		return "", err
	}

	return reference.Object.SHA, nil
}

func (g *GitHub) branch(ctx context.Context, repository, branch, sha string) error {
	payload := map[string]string{"ref": "refs/heads/" + branch, "sha": sha}

	err := g.call(ctx, http.MethodPost, "/repos/"+repository+"/git/refs", payload, nil)
	if err != nil && strings.Contains(err.Error(), "Reference already exists") {
		return nil
	}

	return err
}

func (g *GitHub) commit(ctx context.Context, repository, branch string, file File, message string) error {
	payload := map[string]any{
		"message": message,
		"content": base64.StdEncoding.EncodeToString([]byte(file.Content)),
		"branch":  branch,
	}

	if sha, found := g.fileSHA(ctx, repository, branch, file.Path); found {
		payload["sha"] = sha
	}

	endpoint := fmt.Sprintf("/repos/%s/contents/%s", repository, file.Path)

	return g.call(ctx, http.MethodPut, endpoint, payload, nil)
}

func (g *GitHub) fileSHA(ctx context.Context, repository, branch, path string) (string, bool) {
	var existing struct {
		SHA string `json:"sha"`
	}

	endpoint := fmt.Sprintf("/repos/%s/contents/%s?ref=%s", repository, path, url.QueryEscape(branch))
	if err := g.call(ctx, http.MethodGet, endpoint, nil, &existing); err != nil {
		return "", false
	}

	return existing.SHA, existing.SHA != ""
}

func (g *GitHub) pull(
	ctx context.Context,
	repository, branch, base, title, body, author string,
) (Change, error) {
	payload := map[string]string{
		"title": title,
		"head":  branch,
		"base":  base,
		"body":  body,
	}

	var pull struct {
		Number    int       `json:"number"`
		Title     string    `json:"title"`
		HTMLURL   string    `json:"html_url"`
		State     string    `json:"state"`
		CreatedAt time.Time `json:"created_at"`
	}

	if err := g.call(ctx, http.MethodPost, "/repos/"+repository+"/pulls", payload, &pull); err != nil {
		return Change{}, err
	}

	return Change{
		ID:       fmt.Sprintf("%d", pull.Number),
		Title:    pull.Title,
		Branch:   branch,
		URL:      pull.HTMLURL,
		Author:   author,
		State:    pull.State,
		OpenedAt: pull.CreatedAt,
	}, nil
}

func (g *GitHub) call(ctx context.Context, method, endpoint string, payload, into any) error {
	var body *bytes.Reader

	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		body = bytes.NewReader(encoded)
	} else {
		body = bytes.NewReader(nil)
	}

	request, err := http.NewRequestWithContext(ctx, method, g.baseURL+endpoint, body)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}

	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "Bearer "+g.token)
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := g.client.Do(request)
	if err != nil {
		return fmt.Errorf("call the forge: %w", err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode >= 400 {
		var failure struct {
			Message string `json:"message"`
			Errors  []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		_ = json.NewDecoder(response.Body).Decode(&failure)

		detail := failure.Message
		for _, item := range failure.Errors {
			if item.Message != "" {
				detail += ": " + item.Message
			}
		}
		if detail == "" {
			detail = response.Status
		}

		return fmt.Errorf("the forge refused %s %s: %s", method, endpoint, detail)
	}

	if into == nil {
		return nil
	}

	if err := json.NewDecoder(response.Body).Decode(into); err != nil {
		return fmt.Errorf("decode the forge's answer: %w", err)
	}

	return nil
}
