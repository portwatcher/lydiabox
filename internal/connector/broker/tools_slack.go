package broker

import (
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/lydiabox/lydiabox/internal/connector"
)

var (
	slackChannelRe = regexp.MustCompile(`^[#]?[A-Za-z0-9._-]{1,80}$`)
	slackTSRe      = regexp.MustCompile(`^[0-9]{1,12}\.[0-9]{1,8}$`)
)

func slackTools() []tool {
	return []tool{
		{name: "slack_list_channels", provider: connector.Slack,
			description: "List Slack channels the connected account can see.",
			props: map[string]any{"types": prop("string", "Comma-separated conversation types (default public_channel,private_channel)"),
				"limit":  prop("integer", "Maximum channels (1-1000, default 200)"),
				"cursor": prop("string", "Pagination cursor from a previous call")},
			run: slackListChannels},
		{name: "slack_post_message", provider: connector.Slack,
			description: "Post a message to a Slack channel (ID or name), optionally in a thread.",
			props: map[string]any{"channel": prop("string", "Channel ID or name"), "text": prop("string", "Message text (mrkdwn)"),
				"thread_ts": prop("string", "Parent message timestamp to reply in a thread")},
			required: []string{"channel", "text"}, run: slackPostMessage},
		{name: "slack_search_messages", provider: connector.Slack,
			description: "Search Slack messages (needs a user token with search:read).",
			props: map[string]any{"query": prop("string", "Slack search query"),
				"count": prop("integer", "Maximum results (1-100, default 20)")},
			required: []string{"query"}, run: slackSearch},
	}
}

func slackListChannels(c *call, raw json.RawMessage) (any, error) {
	var a struct {
		Types  string `json:"types"`
		Limit  int    `json:"limit"`
		Cursor string `json:"cursor"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	if a.Types == "" {
		a.Types = "public_channel,private_channel"
	}
	for _, t := range strings.Split(a.Types, ",") {
		switch t {
		case "public_channel", "private_channel", "mpim", "im":
		default:
			return nil, invalidf("unknown conversation type %q", t)
		}
	}
	q := url.Values{"types": {a.Types}, "limit": {strconv.Itoa(clamp(a.Limit, 200, 1, 1000))}, "exclude_archived": {"true"}}
	if a.Cursor != "" {
		q.Set("cursor", a.Cursor)
	}
	var out struct {
		Channels []struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			IsPrivate  bool   `json:"is_private"`
			IsMember   bool   `json:"is_member"`
			NumMembers int    `json:"num_members"`
			Topic      struct {
				Value string `json:"value"`
			} `json:"topic"`
		} `json:"channels"`
		Meta struct {
			NextCursor string `json:"next_cursor"`
		} `json:"response_metadata"`
	}
	if err := c.do(http.MethodGet, c.p.API("/conversations.list?"+q.Encode()), nil, &out, nil); err != nil {
		return nil, err
	}
	list := []map[string]any{}
	for _, ch := range out.Channels {
		list = append(list, map[string]any{"id": ch.ID, "name": ch.Name, "private": ch.IsPrivate, "member": ch.IsMember,
			"members": ch.NumMembers, "topic": ch.Topic.Value})
	}
	res := map[string]any{"channels": list}
	if out.Meta.NextCursor != "" {
		res["nextCursor"] = out.Meta.NextCursor
	}
	return res, nil
}

func slackPostMessage(c *call, raw json.RawMessage) (any, error) {
	var a struct {
		Channel  string `json:"channel"`
		Text     string `json:"text"`
		ThreadTS string `json:"thread_ts"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	if !slackChannelRe.MatchString(a.Channel) {
		return nil, invalidf("channel %q is not a Slack channel ID or name", a.Channel)
	}
	if strings.TrimSpace(a.Text) == "" || len(a.Text) > 40000 {
		return nil, invalidf("text must be 1 to 40000 characters")
	}
	in := map[string]any{"channel": strings.TrimPrefix(a.Channel, "#"), "text": a.Text}
	if a.ThreadTS != "" {
		if !slackTSRe.MatchString(a.ThreadTS) {
			return nil, invalidf("thread_ts %q is not a Slack timestamp", a.ThreadTS)
		}
		in["thread_ts"] = a.ThreadTS
	}
	var out struct {
		Channel string `json:"channel"`
		TS      string `json:"ts"`
	}
	if err := c.do(http.MethodPost, c.p.API("/chat.postMessage"), in, &out, nil); err != nil {
		return nil, err
	}
	return map[string]any{"channel": out.Channel, "ts": out.TS}, nil
}

func slackSearch(c *call, raw json.RawMessage) (any, error) {
	var a struct {
		Query string `json:"query"`
		Count int    `json:"count"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	if strings.TrimSpace(a.Query) == "" {
		return nil, invalidf("query is required")
	}
	q := url.Values{"query": {a.Query}, "count": {strconv.Itoa(clamp(a.Count, 20, 1, 100))}}
	var out struct {
		Messages struct {
			Total   int `json:"total"`
			Matches []struct {
				Channel struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"channel"`
				User      string `json:"user"`
				Username  string `json:"username"`
				Text      string `json:"text"`
				TS        string `json:"ts"`
				Permalink string `json:"permalink"`
			} `json:"matches"`
		} `json:"messages"`
	}
	if err := c.do(http.MethodGet, c.p.API("/search.messages?"+q.Encode()), nil, &out, nil); err != nil {
		return nil, err
	}
	matches := []map[string]any{}
	for _, m := range out.Messages.Matches {
		matches = append(matches, map[string]any{"channel": m.Channel.Name, "channelId": m.Channel.ID, "user": m.Username,
			"userId": m.User, "text": m.Text, "ts": m.TS, "permalink": m.Permalink})
	}
	return map[string]any{"total": out.Messages.Total, "matches": matches}, nil
}
