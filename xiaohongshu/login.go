package xiaohongshu

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/go-rod/rod"
	"github.com/pkg/errors"
)

type LoginAction struct {
	page *rod.Page
}

func NewLogin(page *rod.Page) *LoginAction {
	return &LoginAction{page: page}
}

func (a *LoginAction) CheckLoginStatus(ctx context.Context) (bool, error) {
	// 加超时保护：只是查登录态的快速检查，不应无限挂（登录扫码的等待在 Login/WaitForLogin 里）
	pp := a.page.Context(ctx).Timeout(30 * time.Second)
	pp.MustNavigate("https://www.xiaohongshu.com/explore").MustWaitLoad()

	time.Sleep(1 * time.Second)

	exists, _, err := pp.Has(`.main-container .user .link-wrapper .channel`)
	if err != nil {
		return false, errors.Wrap(err, "check login status failed")
	}

	if !exists {
		return false, errors.Wrap(err, "login status element not found")
	}

	return true, nil
}

// CurrentUser 当前登录用户的基础信息。
type CurrentUser struct {
	Nickname string `json:"nickname"`
	UserID   string `json:"userId"`
}

// CurrentUser 从当前页面的 __INITIAL_STATE__ 读取登录用户信息。
// 需在 CheckLoginStatus 之后调用：复用已加载的 explore 页，不做额外导航。
func (a *LoginAction) CurrentUser(ctx context.Context) (*CurrentUser, error) {
	pp := a.page.Context(ctx).Timeout(10 * time.Second)

	res, err := pp.Eval(`() => {
		const u = window.__INITIAL_STATE__ && window.__INITIAL_STATE__.user;
		const info = u && u.userInfo && u.userInfo.value !== undefined ? u.userInfo.value : (u && u.userInfo);
		if (!info || info.guest) return "";
		return JSON.stringify({nickname: info.nickname, userId: info.userId || info.user_id});
	}`)
	if err != nil {
		return nil, errors.Wrap(err, "read current user state failed")
	}

	raw := res.Value.String()
	if raw == "" {
		return nil, errors.New("current user not found in page state")
	}

	var user CurrentUser
	if err := json.Unmarshal([]byte(raw), &user); err != nil {
		return nil, errors.Wrap(err, "unmarshal current user failed")
	}

	return &user, nil
}

func (a *LoginAction) Login(ctx context.Context) error {
	pp := a.page.Context(ctx)

	// 导航到小红书首页，这会触发二维码弹窗
	pp.MustNavigate("https://www.xiaohongshu.com/explore").MustWaitLoad()

	time.Sleep(2 * time.Second)

	if exists, _, _ := pp.Has(".main-container .user .link-wrapper .channel"); exists {
		return nil
	}

	pp.MustElement(".main-container .user .link-wrapper .channel")

	return nil
}

func (a *LoginAction) FetchQrcodeImage(ctx context.Context) (string, bool, error) {
	// Keep this bounded: Xiaohongshu occasionally changes or rejects the login
	// page, and an unbounded MustElement call would make the MCP request time out.
	pp := a.page.Context(ctx).Timeout(45 * time.Second)

	// 导航到小红书首页，这会触发二维码弹窗
	if err := pp.Navigate("https://www.xiaohongshu.com/explore"); err != nil {
		return "", false, errors.Wrap(err, "navigate to xiaohongshu login page failed")
	}
	if err := pp.WaitLoad(); err != nil {
		return "", false, errors.Wrap(err, "wait for xiaohongshu login page failed")
	}

	time.Sleep(2 * time.Second)

	if exists, _, _ := pp.Has(".main-container .user .link-wrapper .channel"); exists {
		return "", true, nil
	}

	// Some variants no longer open the login modal automatically. Trigger the
	// visible login entry before looking for the QR image.
	_, _ = pp.Eval(`() => {
		const nodes = [...document.querySelectorAll('button, a, [role=button], div')];
		const login = nodes.find(el => (el.textContent || '').trim() === '登录' && el.getBoundingClientRect().width > 0);
		if (login) { login.click(); return true; }
		return false;
	}`)
	time.Sleep(2 * time.Second)

	// Xiaohongshu has used multiple wrappers and attributes for this image.
	selectors := []string{
		".qrcode-img",
		"[class*=qrcode] img",
		"[class*=qr-code] img",
		"img[alt*=二维码]",
		"img[src^='data:image']",
	}
	var el *rod.Element
	var err error
	for _, selector := range selectors {
		el, err = pp.Timeout(4 * time.Second).Element(selector)
		if err == nil && el != nil {
			break
		}
	}
	if el == nil {
		// A/B variants sometimes paint the QR into a canvas instead of an img.
		canvas, canvasErr := pp.Eval(`() => {
			const el = document.querySelector('[class*=qrcode] canvas, [class*=qr-code] canvas, canvas[aria-label*=二维码]');
			if (!el || !el.width || !el.height) return '';
			try { return el.toDataURL('image/png'); } catch (_) { return ''; }
		}`)
		if canvasErr == nil && canvas.Value.String() != "" {
			return canvas.Value.String(), false, nil
		}

		// Return a compact diagnostic instead of an opaque timeout so the next
		// selector update can be based on the page actually served to Render.
		pageURL := ""
		if info, infoErr := pp.Info(); infoErr == nil {
			pageURL = info.URL
		}
		bodyText := ""
		if body, bodyErr := pp.Eval(`() => (document.body?.innerText || '').replace(/\s+/g, ' ').slice(0, 500)`); bodyErr == nil {
			bodyText = body.Value.String()
		}
		// Preserve what the server actually saw. The caller labels this as a
		// diagnostic page screenshot, never as a scannable QR code.
		screenshot := ""
		if png, shotErr := pp.Screenshot(false, nil); shotErr == nil && len(png) > 0 {
			screenshot = "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
		}
		return screenshot, false, errors.Wrapf(err, "qrcode element not found (url=%s, page=%q)", pageURL, bodyText)
	}
	src, err := el.Attribute("src")
	if err != nil {
		return "", false, errors.Wrap(err, "get qrcode src failed")
	}
	if src == nil || len(*src) == 0 {
		return "", false, errors.New("qrcode src is empty")
	}

	return *src, false, nil
}

func (a *LoginAction) WaitForLogin(ctx context.Context) bool {
	pp := a.page.Context(ctx)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
			el, err := pp.Element(".main-container .user .link-wrapper .channel")
			if err == nil && el != nil {
				return true
			}
		}
	}
}
