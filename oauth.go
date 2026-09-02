package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type oauthPayload struct {
	ClientID    string `json:"client_id"`
	RedirectURI string `json:"redirect_uri"`
	Challenge   string `json:"challenge,omitempty"`
	Scope       string `json:"scope,omitempty"`
	ExpiresAt   int64  `json:"exp"`
	Kind        string `json:"kind"`
}

type OAuthServer struct {
	password []byte
	secret   []byte
}

func NewOAuthServer(password, secret string) *OAuthServer {
	if password == "" || secret == "" {
		return nil
	}
	return &OAuthServer{password: []byte(password), secret: []byte(secret)}
}

func (s *OAuthServer) enabled() bool {
	return s != nil && len(s.password) > 0 && len(s.secret) > 0
}

func (s *OAuthServer) sign(payload oauthPayload) (string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, s.secret)
	mac.Write(body)
	return base64.RawURLEncoding.EncodeToString(body) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (s *OAuthServer) verify(value, expectedKind string) (oauthPayload, bool) {
	var payload oauthPayload
	parts := strings.Split(value, ".")
	if len(parts) != 2 {
		return payload, false
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return payload, false
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return payload, false
	}
	mac := hmac.New(sha256.New, s.secret)
	mac.Write(body)
	if !hmac.Equal(signature, mac.Sum(nil)) || json.Unmarshal(body, &payload) != nil ||
		payload.Kind != expectedKind || payload.ExpiresAt < time.Now().Unix() {
		return oauthPayload{}, false
	}
	return payload, true
}

func chatGPTRedirectURI(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "chatgpt.com" || strings.HasSuffix(host, ".chatgpt.com") ||
		host == "chat.openai.com" || strings.HasSuffix(host, ".chat.openai.com")
}

func requestBaseURL(c *gin.Context) string {
	scheme := c.GetHeader("X-Forwarded-Proto")
	if scheme == "" {
		scheme = "https"
	}
	return scheme + "://" + c.Request.Host
}

func (s *OAuthServer) authorizationServerMetadata(c *gin.Context) {
	if !s.enabled() {
		c.Status(http.StatusNotFound)
		return
	}
	base := requestBaseURL(c)
	c.JSON(http.StatusOK, gin.H{
		"issuer": base,
		"authorization_endpoint": base + "/oauth/authorize",
		"token_endpoint": base + "/oauth/token",
		"response_types_supported": []string{"code"},
		"grant_types_supported": []string{"authorization_code"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"code_challenge_methods_supported": []string{"S256"},
	})
}

func (s *OAuthServer) protectedResourceMetadata(c *gin.Context) {
	if !s.enabled() {
		c.Status(http.StatusNotFound)
		return
	}
	base := requestBaseURL(c)
	c.JSON(http.StatusOK, gin.H{
		"resource": base + "/mcp",
		"authorization_servers": []string{base},
		"bearer_methods_supported": []string{"header"},
	})
}

var authorizeTemplate = template.Must(template.New("authorize").Parse(`<!doctype html><html><head><meta name="viewport" content="width=device-width,initial-scale=1"><title>Authorize Xiaohongshu MCP</title></head><body><main style="max-width:420px;margin:48px auto;font-family:system-ui"><h2>授权 Xiaohongshu MCP</h2><p>输入你为此服务设置的授权口令，继续连接 ChatGPT。</p>{{if .Error}}<p style="color:#b00020">{{.Error}}</p>{{end}}<form method="post"><input type="hidden" name="client_id" value="{{.ClientID}}"><input type="hidden" name="redirect_uri" value="{{.RedirectURI}}"><input type="hidden" name="state" value="{{.State}}"><input type="hidden" name="scope" value="{{.Scope}}"><input type="hidden" name="code_challenge" value="{{.Challenge}}"><input type="hidden" name="code_challenge_method" value="{{.ChallengeMethod}}"><label>授权口令<br><input required type="password" name="password" autofocus style="width:100%;padding:10px;margin-top:8px"></label><button style="margin-top:16px;padding:10px 16px" type="submit">授权</button></form></main></body></html>`))

func (s *OAuthServer) authorize(c *gin.Context) {
	if !s.enabled() {
		c.Status(http.StatusNotFound)
		return
	}
	if c.Request.Method == http.MethodGet {
		s.renderAuthorize(c, "")
		return
	}
	if subtle.ConstantTimeCompare([]byte(c.PostForm("password")), s.password) != 1 {
		s.renderAuthorize(c, "授权口令不正确")
		return
	}
	clientID := c.PostForm("client_id")
	redirectURI := c.PostForm("redirect_uri")
	challenge := c.PostForm("code_challenge")
	method := c.PostForm("code_challenge_method")
	if clientID == "" || !chatGPTRedirectURI(redirectURI) || challenge == "" || method != "S256" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
		return
	}
	code, err := s.sign(oauthPayload{ClientID: clientID, RedirectURI: redirectURI, Challenge: challenge, Scope: c.PostForm("scope"), ExpiresAt: time.Now().Add(5 * time.Minute).Unix(), Kind: "code"})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "server_error"})
		return
	}
	u, _ := url.Parse(redirectURI)
	q := u.Query()
	q.Set("code", code)
	if state := c.PostForm("state"); state != "" {
		q.Set("state", state)
	}
	u.RawQuery = q.Encode()
	c.Redirect(http.StatusFound, u.String())
}

func (s *OAuthServer) renderAuthorize(c *gin.Context, message string) {
	_ = authorizeTemplate.Execute(c.Writer, gin.H{
		"ClientID": c.Query("client_id"), "RedirectURI": c.Query("redirect_uri"),
		"State": c.Query("state"), "Scope": c.Query("scope"),
		"Challenge": c.Query("code_challenge"), "ChallengeMethod": c.Query("code_challenge_method"),
		"Error": message,
	})
}

func (s *OAuthServer) token(c *gin.Context) {
	if !s.enabled() {
		c.Status(http.StatusNotFound)
		return
	}
	if c.PostForm("grant_type") != "authorization_code" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported_grant_type"})
		return
	}
	code, ok := s.verify(c.PostForm("code"), "code")
	if !ok || code.ClientID != c.PostForm("client_id") || code.RedirectURI != c.PostForm("redirect_uri") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_grant"})
		return
	}
	sum := sha256.Sum256([]byte(c.PostForm("code_verifier")))
	if base64.RawURLEncoding.EncodeToString(sum[:]) != code.Challenge {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_grant"})
		return
	}
	accessToken, err := s.sign(oauthPayload{ClientID: code.ClientID, Scope: code.Scope, ExpiresAt: time.Now().Add(time.Hour).Unix(), Kind: "access"})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "server_error"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"access_token": accessToken, "token_type": "Bearer", "expires_in": 3600, "scope": code.Scope})
}

func (s *OAuthServer) validAccessToken(token string) bool {
	if !s.enabled() {
		return false
	}
	_, ok := s.verify(token, "access")
	return ok
}
