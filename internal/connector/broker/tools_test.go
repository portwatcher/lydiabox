package broker_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portwatcher/lydiabox/internal/connector"
	"github.com/portwatcher/lydiabox/internal/connector/connectortest"
	"github.com/portwatcher/lydiabox/internal/store"
)

func TestGitHubTools(t *testing.T) {
	e := newEnv(t, connector.GitHub)
	e.mustTool(t, "github_list_repos", nil, "octo/hello")
	text := e.mustTool(t, "github_list_issues", map[string]any{"owner": "octo", "repo": "hello"}, `"number": 1`, `"bug"`)
	if strings.Contains(text, "A pull request") {
		t.Fatalf("issues must exclude pull requests: %s", text)
	}
	e.mustTool(t, "github_create_issue", map[string]any{"owner": "octo", "repo": "hello", "title": "New", "labels": []string{"bug"}}, `"number": 3`)
	if b, ok := e.fake.Body("POST api.github.com/repos/octo/hello/issues"); !ok || !strings.Contains(string(b), `"labels":["bug"]`) {
		t.Fatalf("create issue body = %s", b)
	}
	e.mustTool(t, "github_list_pull_requests", map[string]any{"owner": "octo", "repo": "hello"}, `"head": "feature"`)
	e.mustTool(t, "github_create_pull_request", map[string]any{"owner": "octo", "repo": "hello", "title": "PR", "head": "f", "base": "main"}, `"number": 4`)
	e.mustTool(t, "github_get_file_contents", map[string]any{"owner": "octo", "repo": "hello", "path": "README.md"}, `"content": "hello world"`)
	for _, args := range []map[string]any{
		{"owner": "../x", "repo": "hello"},
		{"owner": "octo", "repo": "hello", "path": "a/../../etc"},
	} {
		if text, isErr := e.callTool(t, "github_get_file_contents", args); !isErr || !strings.Contains(text, "invalid arguments") {
			t.Fatalf("%v: %s", args, text)
		}
	}
	entries := e.audit(t, connector.GitHub)
	calls := 0
	for _, a := range entries {
		if a.Kind == connector.KindMCPCall {
			calls++
			if strings.Contains(a.Detail, e.tokens[connector.GitHub]) {
				t.Fatalf("audit leaks the token: %s", a.Detail)
			}
		}
	}
	if calls != 8 {
		t.Fatalf("mcp.call audit entries = %d, want 8", calls)
	}

	// An upstream 401 marks the account revoked.
	e.fake.RevokeToken(e.tokens[connector.GitHub])
	if text, isErr := e.callTool(t, "github_list_repos", nil); !isErr || !strings.Contains(text, "HTTP 401") {
		t.Fatalf("revoked: %s", text)
	}
	c, _ := e.mgr.Get(context.Background(), tenantID, connector.GitHub)
	if c.Accounts[0].Status != store.AccountRevoked {
		t.Fatalf("status = %s", c.Accounts[0].Status)
	}
}

func TestGDriveTools(t *testing.T) {
	e := newEnv(t, connector.GDrive)
	e.fake.PutFile("file1", connectortest.DriveFile{Name: "report.pdf", MimeType: "application/pdf", Content: []byte("%PDF-1.7")})
	e.fake.PutFile("doc1", connectortest.DriveFile{Name: "Notes", MimeType: "application/vnd.google-apps.document", Content: []byte("notes")})
	e.mustTool(t, "gdrive_search", map[string]any{"query": "it's"}, "report.pdf")

	e.mustTool(t, "gdrive_download_to_workspace", map[string]any{"file_id": "file1", "path": "docs/report.pdf"}, `"path": "docs/report.pdf"`)
	if b, err := os.ReadFile(filepath.Join(e.dirs.workspace, "docs", "report.pdf")); err != nil || string(b) != "%PDF-1.7" {
		t.Fatalf("downloaded = %q %v", b, err)
	}
	if text, isErr := e.callTool(t, "gdrive_download_to_workspace", map[string]any{"file_id": "file1", "path": "docs/report.pdf"}); !isErr ||
		!strings.Contains(text, "overwrite") {
		t.Fatalf("existing target: %s", text)
	}
	e.mustTool(t, "gdrive_download_to_workspace", map[string]any{"file_id": "doc1", "path": "docs/"}, `"path": "docs/Notes.txt"`)
	if b, _ := os.ReadFile(filepath.Join(e.dirs.workspace, "docs", "Notes.txt")); string(b) != "EXPORT:notes" {
		t.Fatalf("export = %q", b)
	}

	if err := os.WriteFile(filepath.Join(e.dirs.workspace, "out.txt"), []byte("upload me"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.mustTool(t, "gdrive_upload_from_workspace", map[string]any{"path": "out.txt", "parent_id": "folder1"}, `"name": "out.txt"`, `"size": 9`)
	e.mustTool(t, "gdrive_upload_from_workspace", map[string]any{"path": "out.txt", "file_id": "file1"}, `"id": "file1"`)
	if f, _ := e.fake.File("file1"); string(f.Content) != "upload me" || f.Name != "report.pdf" || f.MimeType != "text/plain; charset=utf-8" {
		t.Fatalf("updated file = %+v", f)
	}
}

func TestGDrivePathTraversal(t *testing.T) {
	e := newEnv(t, connector.GDrive)
	e.fake.PutFile("file1", connectortest.DriveFile{Name: "x", MimeType: "text/plain", Content: []byte("payload")})
	outside := filepath.Join(filepath.Dir(e.dirs.workspace), "outside")
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("host secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(e.dirs.workspace, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(e.dirs.workspace, "secret-link")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"../outside/pwn.txt", "/etc/pwn.txt", "a/../../outside/pwn.txt", "link/pwn.txt", "..\\pwn.txt", ""} {
		text, isErr := e.callTool(t, "gdrive_download_to_workspace", map[string]any{"file_id": "file1", "path": p})
		if !isErr {
			t.Fatalf("download to %q succeeded: %s", p, text)
		}
	}
	for _, p := range []string{"../outside/secret.txt", "/etc/passwd", "link/secret.txt", "secret-link"} {
		text, isErr := e.callTool(t, "gdrive_upload_from_workspace", map[string]any{"path": p})
		if !isErr {
			t.Fatalf("upload of %q succeeded: %s", p, text)
		}
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 1 {
		t.Fatalf("files were written outside the workspace: %v", entries)
	}
	if _, ok := e.fake.Body("POST www.googleapis.com/upload/drive/v3/files"); ok {
		t.Fatal("a rejected upload reached Drive")
	}
}

func TestNotionTools(t *testing.T) {
	e := newEnv(t, connector.Notion)
	e.mustTool(t, "notion_search", map[string]any{"query": "plan", "filter": "page"}, `"title": "Plan"`, connectortest.NotionPageID)
	e.mustTool(t, "notion_append_to_page", map[string]any{"page_id": connectortest.NotionPageID,
		"text": "# Title\n\nfirst line\nsecond line\n- item\n[] todo"}, `"appendedBlocks": 4`)
	blocks := e.fake.NotionBlocks()
	if len(blocks) != 4 || blocks[0]["type"] != "heading_1" || blocks[1]["type"] != "paragraph" || blocks[3]["type"] != "to_do" {
		t.Fatalf("blocks = %v", blocks)
	}
	e.mustTool(t, "notion_read_page", map[string]any{"page_id": connectortest.NotionPageID}, `"title": "Plan"`,
		`# Title\n\nfirst line\nsecond line\n\n- item\n\n- [ ] todo`)
	if text, isErr := e.callTool(t, "notion_read_page", map[string]any{"page_id": "../users"}); !isErr || !strings.Contains(text, "not a Notion ID") {
		t.Fatalf("bad id: %s", text)
	}
}

func TestSlackTools(t *testing.T) {
	e := newEnv(t, connector.Slack)
	e.mustTool(t, "slack_list_channels", nil, `"name": "general"`)
	e.mustTool(t, "slack_post_message", map[string]any{"channel": "#general", "text": "hi"}, `"channel": "general"`, `"ts"`)
	e.mustTool(t, "slack_search_messages", map[string]any{"query": "release"}, "deploy release")
	entries := e.audit(t, connector.Slack)
	if len(entries) != 4 || entries[0].Kind != connector.KindMCPCall || !strings.Contains(entries[0].Detail, `"tool":"slack_search_messages"`) {
		t.Fatalf("audit = %+v", entries)
	}
}
