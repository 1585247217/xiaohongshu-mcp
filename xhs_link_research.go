package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ResearchXHSLinkArgs makes a share link the only identifier the caller needs.
// It performs only read-only operations.
type ResearchXHSLinkArgs struct {
	URL                  string   `json:"url" jsonschema:"小红书分享链接，仅支持 xhslink 或 xiaohongshu 域名"`
	IncludeAuthorProfile *bool    `json:"include_author_profile,omitempty" jsonschema:"是否读取作者公开主页笔记，默认 true"`
	RelatedKeywords      []string `json:"related_keywords,omitempty" jsonschema:"可选：额外搜索的相近主题，最多 3 个，例如 [MCP, 小机]"`
}

func resolveXHSShareTarget(ctx context.Context, rawURL string) (string, string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || !isAllowedXHSURL(parsed) {
		return "", "", fmt.Errorf("链接无效：只接受 https://xhslink.com、https://xhslink.cn 或 https://xiaohongshu.com 的分享链接")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 Version/17.0 Mobile/15E148 Safari/604.1")
	resp, err := xhsHTTPClient().Do(req)
	if err != nil {
		return "", "", fmt.Errorf("解析分享链接失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("解析分享链接失败：HTTP %d", resp.StatusCode)
	}
	feedID, xsecToken := xhsDetailTarget(resp.Request.URL)
	if feedID == "" || xsecToken == "" {
		return "", "", fmt.Errorf("分享链接未提供可读取的笔记凭证")
	}
	return feedID, xsecToken, nil
}

func rawMCPResult(result *MCPToolResult) (json.RawMessage, error) {
	if result == nil || result.IsError || len(result.Content) == 0 {
		if result != nil && len(result.Content) > 0 {
			return nil, fmt.Errorf("%s", result.Content[0].Text)
		}
		return nil, fmt.Errorf("读取失败")
	}
	data := json.RawMessage(result.Content[0].Text)
	if !json.Valid(data) {
		return nil, fmt.Errorf("服务返回了非 JSON 结果")
	}
	return data, nil
}

// researchXHSLink is best-effort after the detail succeeds: a private author
// page or rate-limited search must not hide the note the user asked to read.
func researchXHSLink(ctx context.Context, _ *mcp.CallToolRequest, args ResearchXHSLinkArgs) (*mcp.CallToolResult, any, error) {
	if shareReaderAppServer == nil {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "小红书服务尚未初始化"}}}, nil, nil
	}
	feedID, xsecToken, err := resolveXHSShareTarget(ctx, args.URL)
	if err != nil {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}, nil, nil
	}
	detail, err := rawMCPResult(shareReaderAppServer.handleGetFeedDetail(ctx, map[string]any{
		"feed_id": feedID, "xsec_token": xsecToken, "load_all_comments": false,
	}))
	if err != nil {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "读取笔记失败：" + err.Error()}}}, nil, nil
	}

	result := struct {
		Detail        json.RawMessage            `json:"detail"`
		AuthorProfile json.RawMessage            `json:"author_profile,omitempty"`
		Searches      map[string]json.RawMessage `json:"related_searches,omitempty"`
		Warnings      []string                   `json:"warnings,omitempty"`
	}{Detail: detail}
	includeProfile := args.IncludeAuthorProfile == nil || *args.IncludeAuthorProfile
	var parsed struct {
		Data struct {
			Note struct {
				User struct {
					UserID string `json:"userId"`
				} `json:"user"`
			} `json:"note"`
		} `json:"data"`
	}
	_ = json.Unmarshal(detail, &parsed)
	if includeProfile {
		if parsed.Data.Note.User.UserID == "" {
			result.Warnings = append(result.Warnings, "笔记详情没有返回作者 ID，未读取作者主页")
		} else if profile, profileErr := rawMCPResult(shareReaderAppServer.handleUserProfile(ctx, map[string]any{
			"user_id": parsed.Data.Note.User.UserID, "xsec_token": xsecToken, "tab": "note",
		})); profileErr != nil {
			result.Warnings = append(result.Warnings, "作者主页暂时不可读取："+profileErr.Error())
		} else {
			result.AuthorProfile = profile
		}
	}
	if len(args.RelatedKeywords) > 3 {
		args.RelatedKeywords = args.RelatedKeywords[:3]
		result.Warnings = append(result.Warnings, "相近主题搜索最多执行 3 个，已忽略其余关键词")
	}
	for _, keyword := range args.RelatedKeywords {
		keyword = strings.TrimSpace(keyword)
		if keyword == "" {
			continue
		}
		if len([]rune(keyword)) > 80 {
			result.Warnings = append(result.Warnings, fmt.Sprintf("搜索词过长，已跳过：%s", keyword))
			continue
		}
		search, searchErr := rawMCPResult(shareReaderAppServer.handleSearchFeeds(ctx, SearchFeedsArgs{Keyword: keyword}))
		if searchErr != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("“%s”搜索暂时不可用：%s", keyword, searchErr))
			continue
		}
		if result.Searches == nil {
			result.Searches = make(map[string]json.RawMessage)
		}
		result.Searches[keyword] = search
	}
	output, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(output)}}}, nil, nil
}
