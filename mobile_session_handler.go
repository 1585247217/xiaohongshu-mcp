package main

import (
    "net/http"
    "strings"

    "github.com/gin-gonic/gin"
)

func (s *AppServer) syncMobileSessionHandler(c *gin.Context) {
    var body struct { Cookie string `json:"cookie"` }
    if err := c.ShouldBindJSON(&body); err != nil {
        c.JSON(http.StatusBadRequest, gin.H{"error":"invalid request"}); return
    }
    if err := storeMobileCookieHeader(strings.TrimSpace(body.Cookie)); err != nil {
        c.JSON(http.StatusServiceUnavailable, gin.H{"error":err.Error()}); return
    }
    c.JSON(http.StatusOK, gin.H{"ok":true})
}
