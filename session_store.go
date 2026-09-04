package main

import (
    "crypto/aes"
    "crypto/cipher"
    "crypto/rand"
    "database/sql"
    "encoding/base64"
    "encoding/json"
    "fmt"
    "io"
    "os"
    "strings"
    "sync"

    _ "github.com/lib/pq"
    "github.com/xpzouying/xiaohongshu-mcp/cookies"
)

const mobileSessionID = "primary"

type encryptedSessionStore struct {
    db  *sql.DB
    gcm cipher.AEAD
}

var mobileSessions struct {
    sync.RWMutex
    store *encryptedSessionStore
}

func initMobileSessionStore() error {
    dsn := strings.TrimSpace(os.Getenv("DATABASE_URL"))
    keyText := strings.TrimSpace(os.Getenv("SESSION_ENCRYPTION_KEY"))
    if dsn == "" || keyText == "" {
        return nil
    }
    key, err := base64.StdEncoding.DecodeString(keyText)
    if err != nil || len(key) != 32 {
        return fmt.Errorf("SESSION_ENCRYPTION_KEY must be base64-encoded 32 bytes")
    }
    block, err := aes.NewCipher(key)
    if err != nil { return err }
    gcm, err := cipher.NewGCM(block)
    if err != nil { return err }
    db, err := sql.Open("postgres", dsn)
    if err != nil { return err }
    if err = db.Ping(); err != nil { db.Close(); return err }
    if _, err = db.Exec(`CREATE TABLE IF NOT EXISTS xhs_mobile_session (
        id TEXT PRIMARY KEY,
        payload BYTEA NOT NULL,
        updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
    )`); err != nil { db.Close(); return err }
    mobileSessions.Lock()
    mobileSessions.store = &encryptedSessionStore{db: db, gcm: gcm}
    mobileSessions.Unlock()
    return restoreMobileSession()
}

func (s *encryptedSessionStore) seal(value []byte) ([]byte, error) {
    nonce := make([]byte, s.gcm.NonceSize())
    if _, err := io.ReadFull(rand.Reader, nonce); err != nil { return nil, err }
    return s.gcm.Seal(nonce, nonce, value, nil), nil
}

func (s *encryptedSessionStore) open(value []byte) ([]byte, error) {
    n := s.gcm.NonceSize()
    if len(value) < n { return nil, fmt.Errorf("invalid encrypted mobile session") }
    return s.gcm.Open(nil, value[:n], value[n:], nil)
}

func currentMobileSessionStore() *encryptedSessionStore {
    mobileSessions.RLock(); defer mobileSessions.RUnlock()
    return mobileSessions.store
}

func restoreMobileSession() error {
    store := currentMobileSessionStore()
    if store == nil { return nil }
    var payload []byte
    err := store.db.QueryRow("SELECT payload FROM xhs_mobile_session WHERE id=$1", mobileSessionID).Scan(&payload)
    if err == sql.ErrNoRows { return nil }
    if err != nil { return err }
    plain, err := store.open(payload)
    if err != nil { return err }
    return cookies.NewLoadCookie(cookies.GetCookiesFilePath()).SaveCookies(plain)
}

func storeMobileCookieHeader(header string) error {
    if len(header) == 0 || len(header) > 32*1024 { return fmt.Errorf("invalid Cookie header") }
    parts := strings.Split(header, ";")
    out := make([]map[string]any, 0, len(parts))
    for _, part := range parts {
        pair := strings.SplitN(strings.TrimSpace(part), "=", 2)
        if len(pair) != 2 || pair[0] == "" { continue }
        out = append(out, map[string]any{
            "name": pair[0], "value": pair[1], "domain": ".xiaohongshu.com",
            "path": "/", "secure": true, "httpOnly": false,
        })
    }
    if len(out) == 0 { return fmt.Errorf("no valid cookies received") }
    data, err := json.Marshal(out)
    if err != nil { return err }
    store := currentMobileSessionStore()
    if store == nil { return fmt.Errorf("mobile session storage is not configured") }
    sealed, err := store.seal(data)
    if err != nil { return err }
    if _, err = store.db.Exec(`INSERT INTO xhs_mobile_session (id, payload, updated_at)
        VALUES ($1,$2,NOW()) ON CONFLICT (id) DO UPDATE SET payload=EXCLUDED.payload, updated_at=NOW()`, mobileSessionID, sealed); err != nil { return err }
    return cookies.NewLoadCookie(cookies.GetCookiesFilePath()).SaveCookies(data)
}
