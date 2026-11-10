package broker

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/lydiabox/lydiabox/internal/connector"
)

var (
	githubOwnerRe = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	githubRepoRe  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
)

var githubHeaders = map[string]string{"Accept": "application/vnd.github+json", "X-GitHub-Api-Version": "2022-11-28"}

type repoArgs struct {
	Owner string `json:"owner"`
	Repo  string `json:"repo"`
}

// path returns /repos/{owner}/{repo} after validating both names.
func (a repoArgs) path() (string, error) {
	if !githubOwnerRe.MatchString(a.Owner) {
		return "", invalidf("owner %q is not a GitHub user or organization name", a.Owner)
	}
	if !githubRepoRe.MatchString(a.Repo) || a.Repo == "." || a.Repo == ".." {
		return "", invalidf("repo %q is not a GitHub repository name", a.Repo)
	}
	return "/repos/" + a.Owner + "/" + a.Repo, nil
}

func repoProps(extra map[string]any) map[string]any {
	p := map[string]any{"owner": prop("string", "Repository owner"), "repo": prop("string", "Repository name")}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

func pageQuery(perPage, page int) url.Values {
	q := url.Values{}
	q.Set("per_page", strconv.Itoa(clamp(perPage, 30, 1, 100)))
	q.Set("page", strconv.Itoa(clamp(page, 1, 1, 1000)))
	return q
}

type ghUser struct {
	Login string `json:"login"`
}

func githubTools() []tool {
	pageProps := map[string]any{"per_page": prop("integer", "Results per page (1-100, default 30)"),
		"page": prop("integer", "Page number (default 1)")}
	return []tool{
		{name: "github_list_repos", provider: connector.GitHub,
			description: "List repositories the connected GitHub account can access, most recently updated first.",
			props: map[string]any{"visibility": enumProp("Filter by visibility", "all", "public", "private"),
				"per_page": pageProps["per_page"], "page": pageProps["page"]},
			run: githubListRepos},
		{name: "github_list_issues", provider: connector.GitHub,
			description: "List issues (not pull requests) of a repository.",
			props: repoProps(map[string]any{"state": enumProp("Issue state", "open", "closed", "all"),
				"labels": prop("string", "Comma-separated label names"), "per_page": pageProps["per_page"], "page": pageProps["page"]}),
			required: []string{"owner", "repo"}, run: githubListIssues},
		{name: "github_create_issue", provider: connector.GitHub,
			description: "Create an issue in a repository.",
			props: repoProps(map[string]any{"title": prop("string", "Issue title"), "body": prop("string", "Issue body (Markdown)"),
				"labels": arrayProp("Label names"), "assignees": arrayProp("Assignee logins")}),
			required: []string{"owner", "repo", "title"}, run: githubCreateIssue},
		{name: "github_list_pull_requests", provider: connector.GitHub,
			description: "List pull requests of a repository.",
			props: repoProps(map[string]any{"state": enumProp("Pull request state", "open", "closed", "all"),
				"per_page": pageProps["per_page"], "page": pageProps["page"]}),
			required: []string{"owner", "repo"}, run: githubListPulls},
		{name: "github_create_pull_request", provider: connector.GitHub,
			description: "Open a pull request from head into base.",
			props: repoProps(map[string]any{"title": prop("string", "Pull request title"), "head": prop("string", "Branch with the changes (or owner:branch)"),
				"base": prop("string", "Branch to merge into"), "body": prop("string", "Description (Markdown)"), "draft": prop("boolean", "Open as draft")}),
			required: []string{"owner", "repo", "title", "head", "base"}, run: githubCreatePull},
		{name: "github_get_file_contents", provider: connector.GitHub,
			description: "Read a file (or list a directory) of a repository at an optional ref.",
			props:       repoProps(map[string]any{"path": prop("string", "Path inside the repository"), "ref": prop("string", "Branch, tag, or commit SHA")}),
			required:    []string{"owner", "repo"}, run: githubGetContents},
	}
}

func githubListRepos(c *call, raw json.RawMessage) (any, error) {
	var a struct {
		Visibility string `json:"visibility"`
		PerPage    int    `json:"per_page"`
		Page       int    `json:"page"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	q := pageQuery(a.PerPage, a.Page)
	q.Set("sort", "updated")
	switch a.Visibility {
	case "", "all":
	case "public", "private":
		q.Set("visibility", a.Visibility)
	default:
		return nil, invalidf("visibility must be all, public, or private")
	}
	var repos []struct {
		FullName      string `json:"full_name"`
		Private       bool   `json:"private"`
		Description   string `json:"description"`
		DefaultBranch string `json:"default_branch"`
		HTMLURL       string `json:"html_url"`
		CloneURL      string `json:"clone_url"`
		UpdatedAt     string `json:"updated_at"`
	}
	if err := c.do(http.MethodGet, c.p.API("/user/repos?"+q.Encode()), nil, &repos, githubHeaders); err != nil {
		return nil, err
	}
	return map[string]any{"repositories": repos}, nil
}

func stateArg(s string) (string, error) {
	switch s {
	case "":
		return "open", nil
	case "open", "closed", "all":
		return s, nil
	}
	return "", invalidf("state must be open, closed, or all")
}

func githubListIssues(c *call, raw json.RawMessage) (any, error) {
	var a struct {
		repoArgs
		State   string `json:"state"`
		Labels  string `json:"labels"`
		PerPage int    `json:"per_page"`
		Page    int    `json:"page"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	base, err := a.path()
	if err != nil {
		return nil, err
	}
	q := pageQuery(a.PerPage, a.Page)
	if a.State, err = stateArg(a.State); err != nil {
		return nil, err
	}
	q.Set("state", a.State)
	if a.Labels != "" {
		q.Set("labels", a.Labels)
	}
	var items []struct {
		Number      int             `json:"number"`
		Title       string          `json:"title"`
		State       string          `json:"state"`
		User        ghUser          `json:"user"`
		HTMLURL     string          `json:"html_url"`
		Comments    int             `json:"comments"`
		CreatedAt   string          `json:"created_at"`
		PullRequest json.RawMessage `json:"pull_request"`
		Labels      []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := c.do(http.MethodGet, c.p.API(base+"/issues?"+q.Encode()), nil, &items, githubHeaders); err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, it := range items {
		if len(it.PullRequest) > 0 {
			continue
		}
		labels := []string{}
		for _, l := range it.Labels {
			labels = append(labels, l.Name)
		}
		out = append(out, map[string]any{"number": it.Number, "title": it.Title, "state": it.State, "author": it.User.Login,
			"labels": labels, "comments": it.Comments, "url": it.HTMLURL, "createdAt": it.CreatedAt})
	}
	return map[string]any{"issues": out}, nil
}

type ghCreated struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
	State   string `json:"state"`
}

func githubCreateIssue(c *call, raw json.RawMessage) (any, error) {
	var a struct {
		repoArgs
		Title     string   `json:"title"`
		Body      string   `json:"body"`
		Labels    []string `json:"labels"`
		Assignees []string `json:"assignees"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	base, err := a.path()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(a.Title) == "" {
		return nil, invalidf("title is required")
	}
	in := map[string]any{"title": a.Title, "body": a.Body}
	if len(a.Labels) > 0 {
		in["labels"] = a.Labels
	}
	if len(a.Assignees) > 0 {
		in["assignees"] = a.Assignees
	}
	var out ghCreated
	if err := c.do(http.MethodPost, c.p.API(base+"/issues"), in, &out, githubHeaders); err != nil {
		return nil, err
	}
	return out, nil
}

func githubListPulls(c *call, raw json.RawMessage) (any, error) {
	var a struct {
		repoArgs
		State   string `json:"state"`
		PerPage int    `json:"per_page"`
		Page    int    `json:"page"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	base, err := a.path()
	if err != nil {
		return nil, err
	}
	q := pageQuery(a.PerPage, a.Page)
	if a.State, err = stateArg(a.State); err != nil {
		return nil, err
	}
	q.Set("state", a.State)
	type ref struct {
		Ref string `json:"ref"`
	}
	var pulls []struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		State   string `json:"state"`
		Draft   bool   `json:"draft"`
		User    ghUser `json:"user"`
		Head    ref    `json:"head"`
		Base    ref    `json:"base"`
		HTMLURL string `json:"html_url"`
	}
	if err := c.do(http.MethodGet, c.p.API(base+"/pulls?"+q.Encode()), nil, &pulls, githubHeaders); err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, p := range pulls {
		out = append(out, map[string]any{"number": p.Number, "title": p.Title, "state": p.State, "draft": p.Draft,
			"author": p.User.Login, "head": p.Head.Ref, "base": p.Base.Ref, "url": p.HTMLURL})
	}
	return map[string]any{"pullRequests": out}, nil
}

func githubCreatePull(c *call, raw json.RawMessage) (any, error) {
	var a struct {
		repoArgs
		Title string `json:"title"`
		Head  string `json:"head"`
		Base  string `json:"base"`
		Body  string `json:"body"`
		Draft bool   `json:"draft"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	base, err := a.path()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(a.Title) == "" || a.Head == "" || a.Base == "" {
		return nil, invalidf("title, head, and base are required")
	}
	var out ghCreated
	in := map[string]any{"title": a.Title, "head": a.Head, "base": a.Base, "body": a.Body, "draft": a.Draft}
	if err := c.do(http.MethodPost, c.p.API(base+"/pulls"), in, &out, githubHeaders); err != nil {
		return nil, err
	}
	return out, nil
}

// escapeRepoPath validates a repository path and escapes each segment.
func escapeRepoPath(p string) (string, error) {
	p = strings.Trim(p, "/")
	if p == "" {
		return "", nil
	}
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if s == "" || s == "." || s == ".." || strings.ContainsRune(s, 0) {
			return "", invalidf("path %q is not a valid repository path", p)
		}
		segs[i] = url.PathEscape(s)
	}
	return "/" + strings.Join(segs, "/"), nil
}

func githubGetContents(c *call, raw json.RawMessage) (any, error) {
	var a struct {
		repoArgs
		Path string `json:"path"`
		Ref  string `json:"ref"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	base, err := a.path()
	if err != nil {
		return nil, err
	}
	rel, err := escapeRepoPath(a.Path)
	if err != nil {
		return nil, err
	}
	u := c.p.API(base + "/contents" + rel)
	if a.Ref != "" {
		u += "?" + url.Values{"ref": {a.Ref}}.Encode()
	}
	var body json.RawMessage
	if err := c.do(http.MethodGet, u, nil, &body, githubHeaders); err != nil {
		return nil, err
	}
	type entry struct {
		Name     string `json:"name"`
		Path     string `json:"path"`
		Type     string `json:"type"`
		Size     int64  `json:"size"`
		SHA      string `json:"sha"`
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
	}
	var dir []entry
	if json.Unmarshal(body, &dir) == nil {
		out := make([]map[string]any, 0, len(dir))
		for _, e := range dir {
			out = append(out, map[string]any{"name": e.Name, "path": e.Path, "type": e.Type, "size": e.Size})
		}
		return map[string]any{"type": "dir", "entries": out}, nil
	}
	var f entry
	if err := json.Unmarshal(body, &f); err != nil {
		return nil, &connector.APIError{Provider: connector.GitHub, Message: "unexpected contents response"}
	}
	if f.Type != "file" {
		return map[string]any{"type": f.Type, "path": f.Path, "sha": f.SHA}, nil
	}
	data, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(f.Content, "\n", ""))
	if err != nil || f.Encoding != "base64" {
		return map[string]any{"type": "file", "path": f.Path, "size": f.Size, "sha": f.SHA,
			"note": "content is not inline (file too large); fetch it with git"}, nil
	}
	if !utf8.Valid(data) {
		return map[string]any{"type": "file", "path": f.Path, "size": f.Size, "sha": f.SHA, "encoding": "base64",
			"content": base64.StdEncoding.EncodeToString(data)}, nil
	}
	return map[string]any{"type": "file", "path": f.Path, "size": f.Size, "sha": f.SHA, "encoding": "utf-8", "content": string(data)}, nil
}
