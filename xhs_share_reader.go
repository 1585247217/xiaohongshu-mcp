package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var initialStatePattern = regexp.MustCompile("(?s)window\\.__INITIAL_STATE__\\s*=\\s*(\\{.*?\\})\\s*</script>")

type ReadXHSShareArgs struct {
	URL           string `json:"url" jsonschema:"小红书分享链接，仅支持 xhslink 或 xiaohongshu 域名"`
	IncludeImages *bool  `json:"include_images,omitempty" jsonschema:"是否返回图片直链，默认 true；关闭后只返回文字"`
}

func isAllowedXHSURL(u *url.URL) bool {
	if u == nil || u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "xhslink.com" || host == "xhslink.cn" ||
		host == "xiaohongshu.com" || strings.HasSuffix(host, ".xiaohongshu.com")
}

func xhsHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 5 || !isAllowedXHSURL(req.URL) {
				return fmt.Errorf("redirect target is not an allowed Xiaohongshu URL")
			}
			return nil
		},
	}
}

func xhsMap(value any) map[string]any {
	m, _ := value.(map[string]any)
	return m
}

func xhsNested(m map[string]any, keys ...string) any {
	var value any = m
	for _, key := range keys {
		current := xhsMap(value)
		if current == nil {
			return nil
		}
		value = current[key]
	}
	return value
}

func xhsString(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func xhsNoteFromState(state map[string]any) map[string]any {
	if note := xhsMap(xhsNested(state, "noteData", "data", "noteData")); note != nil {
		return note
	}
	details := xhsMap(xhsNested(state, "note", "noteDetailMap"))
	for _, item := range details {
		if note := xhsMap(xhsNested(xhsMap(item), "note")); note != nil {
			return note
		}
	}
	return nil
}

func xhsCommentsFromState(state map[string]any) []any {
	if comments, ok := xhsNested(state, "noteData", "data", "commentData", "comments").([]any); ok {
		return comments
	}
	details := xhsMap(xhsNested(state, "note", "noteDetailMap"))
	for _, item := range details {
		if comments, ok := xhsNested(xhsMap(item), "comments", "comments").([]any); ok {
			return comments
		}
	}
	return nil
}

func readXHSShareLink(ctx context.Context, args ReadXHSShareArgs) (*mcp.CallToolResult, any, error) {
	parsedURL, err := url.Parse(args.URL)
	if err != nil || !isAllowedXHSURL(parsedURL) {
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: "链接无效：只接受 https://xhslink.com、https://xhslink.cn 或 https://xiaohongshu.com 的分享链接。"}},
		}, nil, nil
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsedURL.String(), nil)
	if err != nil {
		return nil, nil, err
	}
	request.Header.Set("User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 Version/17.0 Mobile/15E148 Safari/604.1")

	response, err := xhsHTTPClient().Do(request)
	if err != nil {
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: "读取分享链接失败：" + err.Error()}},
		}, nil, nil
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("读取分享链接失败：HTTP %d", response.StatusCode)}},
		}, nil, nil
	}

	html, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return nil, nil, err
	}
	match := initialStatePattern.FindSubmatch(html)
	if len(match) != 2 {
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: "无法解析该页面的公开笔记数据。它可能已删除、需要登录，或小红书页面结构已变化。"}},
		}, nil, nil
	}

	var state map[string]any
	stateJSON := strings.ReplaceAll(string(match[1]), "undefined", "null")
	if err := json.Unmarshal([]byte(stateJSON), &state); err != nil {
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: "公开笔记数据解析失败：" + err.Error()}},
		}, nil, nil
	}
	note := xhsNoteFromState(state)
	if note == nil {
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: "未找到笔记数据。它可能已删除、不可见或链接无效。"}},
		}, nil, nil
	}

	user := xhsMap(note["user"])
	interact := xhsMap(note["interactInfo"])
	var output strings.Builder
	title := xhsString(note["title"])
	if title == "" {
		title = "（无标题）"
	}
	fmt.Fprintf(&output, "标题：%s\\n作者：%s\\n互动：赞 %s · 收藏 %s · 评论 %s\\n\\n%s",
		title,
		firstNonEmpty(xhsString(user["nickName"]), xhsString(user["nickname"]), "未知"),
		firstNonEmpty(xhsString(interact["likedCount"]), "0"),
		firstNonEmpty(xhsString(interact["collectedCount"]), "0"),
		firstNonEmpty(xhsString(interact["commentCount"]), "0"),
		xhsString(note["desc"]),
	)
	if xhsString(note["type"]) == "video" {
		output.WriteString("\\n\\n这是一则视频笔记；只返回公开文字和封面信息。")
	}

	if comments := xhsCommentsFromState(state); len(comments) > 0 {
		output.WriteString("\\n\\n首屏评论：")
		for i, rawComment := range comments {
			if i >= 20 {
				break
			}
			comment := xhsMap(rawComment)
			commentUser := xhsMap(comment["user"])
			fmt.Fprintf(&output, "\\n- %s：%s",
				firstNonEmpty(xhsString(commentUser["nickName"]), xhsString(commentUser["nickname"]), "匿名"),
				xhsString(comment["content"]),
			)
		}
	}

	includeImages := true
	if args.IncludeImages != nil {
		includeImages = *args.IncludeImages
	}
	if includeImages {
		if images, ok := note["imageList"].([]any); ok && len(images) > 0 {
			output.WriteString("\\n\\n图片链接：")
			for i, rawImage := range images {
				if i >= 9 {
					break
				}
				image := xhsMap(rawImage)
				imageURL := firstNonEmpty(xhsString(image["url"]), xhsString(image["urlDefault"]))
				if strings.HasPrefix(imageURL, "//") {
					imageURL = "https:" + imageURL
				}
				if strings.HasPrefix(imageURL, "https://") {
					output.WriteString("\\n- " + imageURL)
				}
			}
		}
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: output.String()}},
	}, nil, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
