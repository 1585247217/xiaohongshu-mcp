package main

import (
    "flag"
    "os"
    "strings"

    "github.com/sirupsen/logrus"
    "github.com/xpzouying/xiaohongshu-mcp/browser"
    "github.com/xpzouying/xiaohongshu-mcp/configs"
    "github.com/xpzouying/xiaohongshu-mcp/cookies"
)

// Bump this whenever MCP's externally visible tool schema changes.  Chat
// clients may cache a connector's tool catalog by the server version, so a
// stable "dev" value can leave an already-reconnected client on stale tools.
var version = "2026.09.05-mobile-agent-rpc.1"

func main() {
    var ( headless bool; port string; token string )
    defaultPort := ":18060"
    if renderPort := strings.TrimSpace(os.Getenv("PORT")); renderPort != "" {
        if strings.HasPrefix(renderPort, ":") { defaultPort = renderPort } else { defaultPort = ":" + renderPort }
    }
    flag.BoolVar(&headless, "headless", true, "是否无头模式")
    flag.StringVar(&port, "port", defaultPort, "端口")
    flag.StringVar(&token, "token", "", "鉴权 Token，留空则读取 AUTH_TOKEN")
    flag.Parse()
    if token == "" { token = os.Getenv("AUTH_TOKEN") }
    logrus.Infof("xiaohongshu-mcp version: %s", version)

    if err := initMobileSessionStore(); err != nil {
        logrus.Fatalf("mobile session storage initialization failed: %v", err)
    }

    binPath, err := browser.EnsureBrowser()
    if err != nil { logrus.Fatalf("%v", err) }
    logrus.Infof("using browser binary: %s", binPath)
    configs.InitHeadless(headless)
    configs.SetFingerprintSeed(configs.ResolveFingerprintSeed(cookies.NewLoadCookie(cookies.GetCookiesFilePath())))
    configs.SetProxy(configs.ProxyFromEnv())

    appServer := NewAppServer(NewXiaohongshuService(), token)
    if err := appServer.Start(port); err != nil { logrus.Fatalf("failed to run server: %v", err) }
}
