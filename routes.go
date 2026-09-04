package main

import (
    "net/http"

    "github.com/gin-gonic/gin"
    "github.com/modelcontextprotocol/go-sdk/mcp"
)

func setupRoutes(appServer *AppServer) *gin.Engine {
    gin.SetMode(gin.ReleaseMode)
    router := gin.New()
    router.Use(gin.Logger())
    router.Use(gin.Recovery())
    router.Use(errorHandlingMiddleware())
    router.Use(corsMiddleware())
    router.GET("/health", healthHandler)
    router.GET("/.well-known/oauth-authorization-server", appServer.oauthServer.authorizationServerMetadata)
    router.GET("/.well-known/oauth-protected-resource", appServer.oauthServer.protectedResourceMetadata)
    router.GET("/.well-known/oauth-protected-resource/:resource", appServer.oauthServer.protectedResourceMetadata)
    router.GET("/oauth/authorize", appServer.oauthServer.authorize)
    router.POST("/oauth/authorize", appServer.oauthServer.authorize)
    router.POST("/oauth/token", appServer.oauthServer.token)

    mcpHandler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server { return appServer.mcpServer },
        &mcp.StreamableHTTPOptions{JSONResponse: true, DisableLocalhostProtection: true, Stateless: true})
    protected := router.Group("")
    protected.Use(authMiddleware(appServer.authToken, appServer.oauthServer))
    protected.Any("/mcp", gin.WrapH(mcpHandler))
    protected.Any("/mcp/*path", gin.WrapH(mcpHandler))
    api := protected.Group("/api/v1")
    {
        api.POST("/mobile/session", appServer.syncMobileSessionHandler)
        api.GET("/login/status", appServer.checkLoginStatusHandler)
        api.GET("/login/qrcode", appServer.getLoginQrcodeHandler)
        api.DELETE("/login/cookies", appServer.deleteCookiesHandler)
        api.POST("/publish", appServer.publishHandler)
        api.POST("/publish_video", appServer.publishVideoHandler)
        api.GET("/feeds/list", appServer.listFeedsHandler)
        api.GET("/feeds/search", appServer.searchFeedsHandler)
        api.POST("/feeds/search", appServer.searchFeedsHandler)
        api.POST("/feeds/detail", appServer.getFeedDetailHandler)
        api.POST("/user/profile", appServer.userProfileHandler)
        api.POST("/feeds/comment", appServer.postCommentHandler)
        api.POST("/feeds/comment/reply", appServer.replyCommentHandler)
        api.POST("/feeds/like", appServer.likeFeedHandler)
        api.POST("/feeds/favorite", appServer.favoriteFeedHandler)
        api.GET("/user/me", appServer.myProfileHandler)
        api.GET("/notifications/unread", appServer.getUnreadCountHandler)
        api.GET("/notifications/list", appServer.listNotificationsHandler)
        api.POST("/notifications/list", appServer.listNotificationsHandler)
        api.POST("/notifications/reply", appServer.replyNotificationHandler)
        api.POST("/notifications/like", appServer.likeNotificationHandler)
    }
    return router
}
