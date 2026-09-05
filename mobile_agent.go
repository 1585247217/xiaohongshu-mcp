package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MobileAgentJob is read-only: the phone may read its own Xiaohongshu session,
// but it never receives account-modifying commands.
type MobileAgentJob struct {
	ID string `json:"id"`
	Kind string `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

type ReadMobilePrivateArgs struct {
	Kind string `json:"kind" jsonschema:"读取类型：profile（我的收藏/点赞）或 attachment（附件页面）"`
	Tab string `json:"tab,omitempty" jsonschema:"profile 时可选 fav 或 liked"`
	URL string `json:"url,omitempty" jsonschema:"attachment 时填写小红书附件页或笔记链接"`
}

func newMobileJobID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil { return "", err }
	return hex.EncodeToString(b), nil
}

func enqueueMobileAgentJob(kind string, payload any) (string, error) {
	store := currentMobileSessionStore()
	if store == nil { return "", fmt.Errorf("手机代理存储尚未配置") }
	id, err := newMobileJobID()
	if err != nil { return "", err }
	raw, err := json.Marshal(payload)
	if err != nil { return "", err }
	_, err = store.db.Exec(`INSERT INTO xhs_mobile_agent_job (id, kind, payload, state, created_at)
		VALUES ($1,$2,$3,'pending',NOW())`, id, kind, raw)
	return id, err
}

func nextMobileAgentJob() (*MobileAgentJob, error) {
	store := currentMobileSessionStore()
	if store == nil { return nil, fmt.Errorf("手机代理存储尚未配置") }
	tx, err := store.db.Begin()
	if err != nil { return nil, err }
	defer tx.Rollback()
	var job MobileAgentJob
	err = tx.QueryRow(`SELECT id, kind, payload FROM xhs_mobile_agent_job
		WHERE state='pending' AND created_at > NOW() - INTERVAL '10 minutes'
		ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&job.ID, &job.Kind, &job.Payload)
	if err == sql.ErrNoRows { return nil, nil }
	if err != nil { return nil, err }
	if _, err = tx.Exec(`UPDATE xhs_mobile_agent_job SET state='running', started_at=NOW() WHERE id=$1`, job.ID); err != nil { return nil, err }
	return &job, tx.Commit()
}

func mobileAgentNextJobHandler(c *gin.Context) {
	job, err := nextMobileAgentJob()
	if err != nil { c.JSON(http.StatusServiceUnavailable, gin.H{"error":err.Error()}); return }
	if job == nil { c.Status(http.StatusNoContent); return }
	c.JSON(http.StatusOK, job)
}

func mobileAgentResultHandler(c *gin.Context) {
	var body struct { Result json.RawMessage `json:"result"`; Error string `json:"error"` }
	if err := c.ShouldBindJSON(&body); err != nil { c.JSON(http.StatusBadRequest, gin.H{"error":"invalid request"}); return }
	if len(body.Result) == 0 { body.Result = json.RawMessage(`null`) }
	store := currentMobileSessionStore()
	if store == nil { c.JSON(http.StatusServiceUnavailable, gin.H{"error":"手机代理存储尚未配置"}); return }
	res, err := store.db.Exec(`UPDATE xhs_mobile_agent_job SET state=CASE WHEN $2='' THEN 'done' ELSE 'failed' END,
		result=$3, error=$2, finished_at=NOW() WHERE id=$1 AND state='running'`, c.Param("id"), strings.TrimSpace(body.Error), body.Result)
	if err != nil { c.JSON(http.StatusInternalServerError, gin.H{"error":err.Error()}); return }
	n, _ := res.RowsAffected()
	if n != 1 { c.JSON(http.StatusNotFound, gin.H{"error":"job not found"}); return }
	c.JSON(http.StatusOK, gin.H{"ok":true})
}

func readMobilePrivate(ctx context.Context, _ *mcp.CallToolRequest, args ReadMobilePrivateArgs) (*mcp.CallToolResult, any, error) {
	kind := strings.TrimSpace(strings.ToLower(args.Kind))
	payload := map[string]string{}
	switch kind {
	case "profile":
		tab := strings.TrimSpace(strings.ToLower(args.Tab))
		if tab != "fav" && tab != "liked" { return &mcp.CallToolResult{IsError:true, Content:[]mcp.Content{&mcp.TextContent{Text:"profile 只能读取 fav 或 liked"}}}, nil, nil }
		payload["tab"] = tab
	case "attachment":
		if strings.TrimSpace(args.URL) == "" { return &mcp.CallToolResult{IsError:true, Content:[]mcp.Content{&mcp.TextContent{Text:"attachment 需要 URL"}}}, nil, nil }
		payload["url"] = args.URL
	default:
		return &mcp.CallToolResult{IsError:true, Content:[]mcp.Content{&mcp.TextContent{Text:"kind 只能是 profile 或 attachment"}}}, nil, nil
	}
	id, err := enqueueMobileAgentJob(kind, payload)
	if err != nil { return &mcp.CallToolResult{IsError:true, Content:[]mcp.Content{&mcp.TextContent{Text:err.Error()}}}, nil, nil }
	store := currentMobileSessionStore()
	deadline := time.NewTimer(25*time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return &mcp.CallToolResult{Content:[]mcp.Content{&mcp.TextContent{Text:"手机代理任务已排队："+id}}}, nil, nil
		case <-deadline.C:
			return &mcp.CallToolResult{Content:[]mcp.Content{&mcp.TextContent{Text:"手机代理正在读取，任务编号："+id}}}, nil, nil
		case <-tick.C:
			var state, result, jobErr string
			err := store.db.QueryRowContext(context.Background(), `SELECT state, COALESCE(result::text,''), COALESCE(error,'') FROM xhs_mobile_agent_job WHERE id=$1`, id).Scan(&state,&result,&jobErr)
			if err != nil { continue }
			if state == "done" {
				if kind == "attachment" {
					var attachment struct { DownloadURL string `json:"download_url"` }
					if json.Unmarshal([]byte(result), &attachment) == nil && attachment.DownloadURL != "" {
						return readPublicAttachment(ctx, nil, ReadPublicAttachmentArgs{URL: attachment.DownloadURL})
					}
				}
				return &mcp.CallToolResult{Content:[]mcp.Content{&mcp.TextContent{Text:result}}}, nil, nil
			}
			if state == "failed" { return &mcp.CallToolResult{IsError:true,Content:[]mcp.Content{&mcp.TextContent{Text:"手机代理读取失败："+jobErr}}}, nil, nil }
		}
	}
}
