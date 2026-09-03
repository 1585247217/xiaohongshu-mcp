package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ReadPublicAttachmentArgs accepts a direct, public document URL discovered
// in an XHS post. It never executes files and only returns a bounded text
// preview for text-like documents.
type ReadPublicAttachmentArgs struct {
	URL string `json:"url" jsonschema:"小红书帖子中发现的公开附件直链（HTTP/HTTPS）"`
}

func publicDocumentURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil {
		return nil, fmt.Errorf("只接受有效的公开 HTTP/HTTPS 链接")
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()) {
		return nil, fmt.Errorf("不读取内网或本机地址")
	}
	return u, nil
}

func attachmentKind(u *url.URL, contentType string) string {
	ext := strings.ToLower(path.Ext(u.Path))
	if ext != "" {
		return strings.TrimPrefix(ext, ".") + " 文件"
	}
	contentType = strings.ToLower(contentType)
	if strings.Contains(contentType, "pdf") { return "PDF 文件" }
	if strings.Contains(contentType, "text") || strings.Contains(contentType, "json") || strings.Contains(contentType, "xml") || strings.Contains(contentType, "csv") { return "文本文件" }
	return "公开附件"
}

func readPublicAttachment(ctx context.Context, _ *mcp.CallToolRequest, args ReadPublicAttachmentArgs) (*mcp.CallToolResult, any, error) {
	u, err := publicDocumentURL(args.URL)
	if err != nil {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "附件链接无效：" + err.Error()}}}, nil, nil
	}
	client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 5 { return fmt.Errorf("附件跳转次数过多") }
		_, err := publicDocumentURL(req.URL.String())
		return err
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil { return nil, nil, err }
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; XiaohongshuMCP/1.0)")
	resp, err := client.Do(req)
	if err != nil { return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "读取公开附件失败：" + err.Error()}}}, nil, nil }
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 { return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("读取公开附件失败：HTTP %d", resp.StatusCode)}}}, nil, nil }

	contentType := resp.Header.Get("Content-Type")
	kind := attachmentKind(resp.Request.URL, contentType)
	// Do not expose binary payloads to the model. PDF and office formats are
	// identified and linked, while text-like files get a safe 1 MiB preview.
	if !strings.Contains(strings.ToLower(contentType), "text/") && !strings.Contains(strings.ToLower(contentType), "json") && !strings.Contains(strings.ToLower(contentType), "xml") && !strings.Contains(strings.ToLower(contentType), "csv") {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("发现%s（%s）。它是二进制或网页下载页，未执行也未展开内容。公开地址：%s", kind, contentType, resp.Request.URL)}}}, nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil { return nil, nil, err }
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("附件类型：%s\n内容预览：\n%s", kind, string(body))}}}, nil, nil
}
