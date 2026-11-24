package broker

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/portwatcher/lydiabox/internal/connector"
)

var notionHeaders = map[string]string{"Notion-Version": connector.NotionVersion}

// Notion API limits.
const (
	notionMaxRichText = 2000
	notionMaxChildren = 100
)

func notionTools() []tool {
	return []tool{
		{name: "notion_search", provider: connector.Notion,
			description: "Search Notion pages and databases shared with the integration.",
			props: map[string]any{"query": prop("string", "Text to search for"),
				"filter":    enumProp("Only return this object type", "page", "database"),
				"page_size": prop("integer", "Maximum results (1-100, default 20)")},
			run: notionSearch},
		{name: "notion_read_page", provider: connector.Notion,
			description: "Read a Notion page as Markdown-like text.",
			props: map[string]any{"page_id": prop("string", "Page ID (32 hex characters, dashes optional)"),
				"max_blocks": prop("integer", "Maximum blocks to read (default 500, maximum 2000)")},
			required: []string{"page_id"}, run: notionReadPage},
		{name: "notion_append_to_page", provider: connector.Notion,
			description: "Append text to a Notion page. Blank lines separate paragraphs; lines starting with '# ', '## ', " +
				"'### ', '- ', or '[] ' become headings, bullets, or to-dos.",
			props:    map[string]any{"page_id": prop("string", "Page ID"), "text": prop("string", "Text to append")},
			required: []string{"page_id", "text"}, run: notionAppend},
	}
}

// notionID validates a page or block ID and returns it without dashes.
func notionID(s string) (string, error) {
	id := strings.ReplaceAll(strings.TrimSpace(s), "-", "")
	if len(id) != 32 {
		return "", invalidf("%q is not a Notion ID", s)
	}
	for _, r := range id {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return "", invalidf("%q is not a Notion ID", s)
		}
	}
	return id, nil
}

type richText struct {
	PlainText string `json:"plain_text"`
}

func plain(rt []richText) string {
	var b strings.Builder
	for _, t := range rt {
		b.WriteString(t.PlainText)
	}
	return b.String()
}

// notionTitle finds the title of a page (a "title" property) or database.
func notionTitle(obj map[string]json.RawMessage) string {
	var title []richText
	if raw, ok := obj["title"]; ok && json.Unmarshal(raw, &title) == nil {
		return plain(title)
	}
	var props map[string]struct {
		Type  string     `json:"type"`
		Title []richText `json:"title"`
	}
	if raw, ok := obj["properties"]; ok && json.Unmarshal(raw, &props) == nil {
		for _, p := range props {
			if p.Type == "title" {
				return plain(p.Title)
			}
		}
	}
	return ""
}

func notionSearch(c *call, raw json.RawMessage) (any, error) {
	var a struct {
		Query    string `json:"query"`
		Filter   string `json:"filter"`
		PageSize int    `json:"page_size"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	in := map[string]any{"query": a.Query, "page_size": clamp(a.PageSize, 20, 1, 100)}
	switch a.Filter {
	case "":
	case "page", "database":
		in["filter"] = map[string]string{"property": "object", "value": a.Filter}
	default:
		return nil, invalidf("filter must be page or database")
	}
	var out struct {
		Results []map[string]json.RawMessage `json:"results"`
	}
	if err := c.do(http.MethodPost, c.p.API("/v1/search"), in, &out, notionHeaders); err != nil {
		return nil, err
	}
	results := []map[string]string{}
	for _, r := range out.Results {
		var id, object, link, edited string
		_ = json.Unmarshal(r["id"], &id)
		_ = json.Unmarshal(r["object"], &object)
		_ = json.Unmarshal(r["url"], &link)
		_ = json.Unmarshal(r["last_edited_time"], &edited)
		results = append(results, map[string]string{"id": id, "object": object, "title": notionTitle(r), "url": link,
			"lastEditedTime": edited})
	}
	return map[string]any{"results": results}, nil
}

type notionBlock struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	HasChildren bool   `json:"has_children"`
	content     struct {
		RichText []richText `json:"rich_text"`
		Checked  bool       `json:"checked"`
		Language string     `json:"language"`
		Title    string     `json:"title"`
	}
}

func (b *notionBlock) UnmarshalJSON(data []byte) error {
	type plainBlock notionBlock
	if err := json.Unmarshal(data, (*plainBlock)(b)); err != nil {
		return err
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(data, &all); err != nil {
		return err
	}
	if raw, ok := all[b.Type]; ok {
		_ = json.Unmarshal(raw, &b.content)
	}
	return nil
}

func (b *notionBlock) markdown() string {
	text := plain(b.content.RichText)
	switch b.Type {
	case "heading_1":
		return "# " + text
	case "heading_2":
		return "## " + text
	case "heading_3":
		return "### " + text
	case "bulleted_list_item":
		return "- " + text
	case "numbered_list_item":
		return "1. " + text
	case "to_do":
		if b.content.Checked {
			return "- [x] " + text
		}
		return "- [ ] " + text
	case "quote", "callout":
		return "> " + text
	case "code":
		return "```" + b.content.Language + "\n" + text + "\n```"
	case "divider":
		return "---"
	case "child_page", "child_database":
		return "[" + strings.ReplaceAll(b.Type, "_", " ") + ": " + b.content.Title + "] (id " + b.ID + ")"
	}
	return text
}

func notionReadPage(c *call, raw json.RawMessage) (any, error) {
	var a struct {
		PageID    string `json:"page_id"`
		MaxBlocks int    `json:"max_blocks"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	id, err := notionID(a.PageID)
	if err != nil {
		return nil, err
	}
	var page map[string]json.RawMessage
	if err := c.do(http.MethodGet, c.p.API("/v1/pages/"+id), nil, &page, notionHeaders); err != nil {
		return nil, err
	}
	var link string
	_ = json.Unmarshal(page["url"], &link)
	limit := clamp(a.MaxBlocks, 500, 1, 2000)
	var lines []string
	cursor, truncated := "", false
	for {
		q := url.Values{"page_size": {"100"}}
		if cursor != "" {
			q.Set("start_cursor", cursor)
		}
		var res struct {
			Results    []notionBlock `json:"results"`
			HasMore    bool          `json:"has_more"`
			NextCursor string        `json:"next_cursor"`
		}
		if err := c.do(http.MethodGet, c.p.API("/v1/blocks/"+id+"/children?"+q.Encode()), nil, &res, notionHeaders); err != nil {
			return nil, err
		}
		for i := range res.Results {
			if len(lines) >= limit {
				truncated = true
				break
			}
			lines = append(lines, res.Results[i].markdown())
		}
		if truncated || !res.HasMore || res.NextCursor == "" {
			truncated = truncated || (res.HasMore && len(lines) >= limit)
			break
		}
		cursor = res.NextCursor
	}
	return map[string]any{"id": id, "title": notionTitle(page), "url": link, "markdown": strings.Join(lines, "\n\n"),
		"truncated": truncated}, nil
}

// chunkText splits s into pieces of at most n bytes on rune boundaries.
func chunkText(s string, n int) []string {
	var out []string
	for len(s) > n {
		cut := n
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		out = append(out, s[:cut])
		s = s[cut:]
	}
	return append(out, s)
}

func textBlock(typ, text string, extra map[string]any) map[string]any {
	rt := []map[string]any{}
	for _, part := range chunkText(text, notionMaxRichText) {
		rt = append(rt, map[string]any{"type": "text", "text": map[string]string{"content": part}})
	}
	body := map[string]any{"rich_text": rt}
	for k, v := range extra {
		body[k] = v
	}
	return map[string]any{"object": "block", "type": typ, typ: body}
}

// notionBlocks converts text into paragraph, heading, bullet, and to-do blocks.
func notionBlocks(text string) []map[string]any {
	var blocks []map[string]any
	var para []string
	flush := func() {
		if len(para) > 0 {
			blocks = append(blocks, textBlock("paragraph", strings.Join(para, "\n"), nil))
			para = nil
		}
	}
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		switch {
		case strings.TrimSpace(line) == "":
			flush()
		case strings.HasPrefix(line, "### "):
			flush()
			blocks = append(blocks, textBlock("heading_3", line[4:], nil))
		case strings.HasPrefix(line, "## "):
			flush()
			blocks = append(blocks, textBlock("heading_2", line[3:], nil))
		case strings.HasPrefix(line, "# "):
			flush()
			blocks = append(blocks, textBlock("heading_1", line[2:], nil))
		case strings.HasPrefix(line, "- "):
			flush()
			blocks = append(blocks, textBlock("bulleted_list_item", line[2:], nil))
		case strings.HasPrefix(line, "[] "):
			flush()
			blocks = append(blocks, textBlock("to_do", line[3:], map[string]any{"checked": false}))
		default:
			para = append(para, line)
		}
	}
	flush()
	return blocks
}

func notionAppend(c *call, raw json.RawMessage) (any, error) {
	var a struct {
		PageID string `json:"page_id"`
		Text   string `json:"text"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	id, err := notionID(a.PageID)
	if err != nil {
		return nil, err
	}
	blocks := notionBlocks(a.Text)
	if len(blocks) == 0 {
		return nil, invalidf("text is empty")
	}
	if len(blocks) > 1000 {
		return nil, invalidf("text produces more than 1000 blocks")
	}
	for start := 0; start < len(blocks); start += notionMaxChildren {
		end := min(start+notionMaxChildren, len(blocks))
		in := map[string]any{"children": blocks[start:end]}
		if err := c.do(http.MethodPatch, c.p.API("/v1/blocks/"+id+"/children"), in, nil, notionHeaders); err != nil {
			return nil, err
		}
	}
	return map[string]any{"pageId": id, "appendedBlocks": len(blocks)}, nil
}
