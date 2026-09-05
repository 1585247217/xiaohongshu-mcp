package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/net/websocket"
)

type mobileAgentHub struct {
	mu sync.Mutex
	conn *websocket.Conn
	sendMu sync.Mutex
	pending map[string]chan mobileAgentReply
	connectedAt time.Time
}

type mobileAgentReply struct {
	Result json.RawMessage `json:"result"`
	Error string `json:"error"`
}

var liveMobileAgent = &mobileAgentHub{pending: make(map[string]chan mobileAgentReply)}

func (h *mobileAgentHub) online() bool {
	h.mu.Lock(); defer h.mu.Unlock()
	return h.conn != nil
}

func (h *mobileAgentHub) attach(conn *websocket.Conn) {
	h.mu.Lock()
	old := h.conn
	h.conn = conn
	h.connectedAt = time.Now()
	h.mu.Unlock()
	if old != nil { _ = old.Close() }
}

func (h *mobileAgentHub) detach(conn *websocket.Conn) {
	h.mu.Lock()
	if h.conn == conn { h.conn = nil }
	h.mu.Unlock()
}

func (h *mobileAgentHub) deliver(id string, reply mobileAgentReply) {
	h.mu.Lock()
	ch := h.pending[id]
	h.mu.Unlock()
	if ch != nil { select { case ch <- reply: default: } }
}

func (h *mobileAgentHub) request(ctx context.Context, kind string, payload any) (json.RawMessage, error) {
	h.mu.Lock()
	conn := h.conn
	h.mu.Unlock()
	if conn == nil { return nil, fmt.Errorf("PHONE_OFFLINE") }
	id, err := newMobileJobID()
	if err != nil { return nil, err }
	ch := make(chan mobileAgentReply, 1)
	h.mu.Lock(); h.pending[id] = ch; h.mu.Unlock()
	defer func() { h.mu.Lock(); delete(h.pending, id); h.mu.Unlock() }()
	command := map[string]any{"type":"command","id":id,"kind":kind,"payload":payload}
	h.sendMu.Lock()
	err = websocket.JSON.Send(conn, command)
	h.sendMu.Unlock()
	if err != nil { h.detach(conn); return nil, fmt.Errorf("PHONE_OFFLINE") }
	timer := time.NewTimer(24 * time.Second); defer timer.Stop()
	select {
	case <-ctx.Done(): return nil, fmt.Errorf("PHONE_TIMEOUT")
	case <-timer.C: return nil, fmt.Errorf("PHONE_TIMEOUT")
	case reply := <-ch:
		if reply.Error != "" { return nil, fmt.Errorf("%s", reply.Error) }
		return reply.Result, nil
	}
}

// mobileAgentWebSocket is intentionally separate from the HTTP API group.
// The sole credential is checked during the opening handshake; ChatGPT never
// receives it, and no unauthenticated control endpoint exists.
func (s *AppServer) mobileAgentWebSocket(c *gin.Context) {
	token := c.Query("agent_token")
	if token == "" {
		parts := strings.Split(c.GetHeader("Sec-WebSocket-Protocol"), ",")
		if len(parts) > 1 { token = strings.TrimSpace(parts[len(parts)-1]) }
	}
	if s.authToken == "" || subtle.ConstantTimeCompare([]byte(token), []byte(s.authToken)) != 1 {
		c.Status(http.StatusUnauthorized); return
	}
	websocket.Handler(func(conn *websocket.Conn) {
		liveMobileAgent.attach(conn)
		defer liveMobileAgent.detach(conn)
		for {
			var message struct {
				Type string `json:"type"`
				ID string `json:"id"`
				Result json.RawMessage `json:"result"`
				Error string `json:"error"`
			}
			if err := websocket.JSON.Receive(conn, &message); err != nil { return }
			if message.Type == "result" && message.ID != "" {
				liveMobileAgent.deliver(message.ID, mobileAgentReply{Result:message.Result, Error:strings.TrimSpace(message.Error)})
			}
		}
	}).ServeHTTP(c.Writer, c.Request)
}

func phoneAgentStatus(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
	state := "offline"; if liveMobileAgent.online() { state = "online" }
	return &mcp.CallToolResult{Content:[]mcp.Content{&mcp.TextContent{Text:fmt.Sprintf(`{"status":%q}`, state)}}}, nil, nil
}

func getMyFavorites(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
	result, err := liveMobileAgent.request(ctx, "favorites", map[string]string{"tab":"fav"})
	if err != nil { return &mcp.CallToolResult{IsError:true,Content:[]mcp.Content{&mcp.TextContent{Text:err.Error()}}}, nil, nil }
	return &mcp.CallToolResult{Content:[]mcp.Content{&mcp.TextContent{Text:string(result)}}}, nil, nil
}

type ReadAttachmentArgs struct { URL string `json:"url" jsonschema:"小红书附件页或笔记分享链接"` }

func readAttachmentFromPhone(ctx context.Context, _ *mcp.CallToolRequest, args ReadAttachmentArgs) (*mcp.CallToolResult, any, error) {
	u, err := url.Parse(strings.TrimSpace(args.URL))
	if err != nil || !isAllowedXHSURL(u) { return &mcp.CallToolResult{IsError:true,Content:[]mcp.Content{&mcp.TextContent{Text:"只接受小红书附件页或分享链接"}}}, nil, nil }
	result, err := liveMobileAgent.request(ctx, "attachment", map[string]string{"url":u.String()})
	if err != nil { return &mcp.CallToolResult{IsError:true,Content:[]mcp.Content{&mcp.TextContent{Text:err.Error()}}}, nil, nil }
	var attachment struct { DownloadURL string `json:"download_url"` }
	if json.Unmarshal(result, &attachment) == nil && attachment.DownloadURL != "" {
		return readPublicAttachment(ctx, nil, ReadPublicAttachmentArgs{URL:attachment.DownloadURL})
	}
	return &mcp.CallToolResult{Content:[]mcp.Content{&mcp.TextContent{Text:string(result)}}}, nil, nil
}
