package xiaohongshu

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"time"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/pkg/errors"
)

type LoginAction struct {
	page *rod.Page
}

func NewLogin(page *rod.Page) *LoginAction {
	return &LoginAction{page: page}
}

func navigateLoginExplore(ctx context.Context, page *rod.Page) error {
	const exploreURL = "https://www.xiaohongshu.com/explore"

	// The explore page keeps long-lived requests open on some XHS builds, so
	// waiting for the browser load event can consume the entire MCP deadline.
	navCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	pp := page.Context(navCtx)
	navErr := pp.Navigate(exploreURL)
	if navErr == nil {
		return nil
	}

	// Navigation timeouts are expected because XHS keeps the document loading.
	// Stop it with a separate short context and continue inspecting the page.
	stopCtx, stopCancel := context.WithTimeout(ctx, 1*time.Second)
	_ = proto.PageStopLoading{}.Call(page.Context(stopCtx))
	stopCancel()
	return nil
}

func (a *LoginAction) CheckLoginStatus(ctx context.Context) (bool, error) {
	// 加超时保护：只是查登录态的快速检查，不应无限挂（登录扫码的等待在 Login/WaitForLogin 里）
	pp := a.page.Context(ctx)
	if err := navigateLoginExplore(ctx, a.page); err != nil {
		return false, err
	}

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
	pp := a.page.Context(ctx)

	// 导航到小红书首页，这会触发二维码弹窗。页面的 load 事件可能
	// 因长连接一直不结束，因此只等待有界导航并继续使用已渲染文档。
	if err := navigateLoginExplore(ctx, a.page); err != nil {
		return "", false, err
	}

	time.Sleep(1 * time.Second)

	hasCtx, cancelHas := context.WithTimeout(ctx, 1*time.Second)
	exists, _, _ := a.page.Context(hasCtx).Has(".main-container .user .link-wrapper .channel")
	cancelHas()
	if exists {
		return "", true, nil
	}

	// Some variants no longer open the login modal automatically. Trigger the
	// visible login entry before looking for the QR image.
	clickCtx, cancelClick := context.WithTimeout(ctx, 2*time.Second)
	_, _ = a.page.Context(clickCtx).Eval(`() => {
		const nodes = [...document.querySelectorAll('button, a, [role=button], div')];
		const login = nodes.find(el => (el.textContent || '').trim() === '登录' && el.getBoundingClientRect().width > 0);
		if (login) { login.click(); return true; }
		return false;
	}`)
	cancelClick()
	time.Sleep(1 * time.Second)

	// Read every known image/canvas variant in one browser evaluation. Sequential
	// selector waits can consume the MCP deadline even when the QR is already
	// visible in a slightly different A/B-test wrapper.
	qrCtx, cancelQR := context.WithTimeout(ctx, 2*time.Second)
	qr, qrErr := a.page.Context(qrCtx).Eval(`() => {
		const selectors = [
			'.qrcode-img',
			'[class*=qrcode] img',
			'[class*=qr-code] img',
			'img[alt*=二维码]',
			'img[src^="data:image"]'
		];
		for (const selector of selectors) {
			const el = document.querySelector(selector);
			if (el && el.src) return el.src;
		}
		const canvas = document.querySelector('[class*=qrcode] canvas, [class*=qr-code] canvas, canvas[aria-label*=二维码]');
		if (canvas && canvas.width && canvas.height) {
			try { return canvas.toDataURL('image/png'); } catch (_) {}
		}
		return '';
	}`)
	cancelQR()
	if qrErr == nil && qr.Value.String() != "" {
		return qr.Value.String(), false, nil
	}

	// Some builds draw the QR inside a closed component. A full-page screenshot
	// remains scannable and avoids falsely returning an opaque timeout.
	shotCtx, cancelShot := context.WithTimeout(ctx, 2*time.Second)
	png, shotErr := a.page.Context(shotCtx).Screenshot(false, nil)
	cancelShot()
	if shotErr == nil && len(png) > 0 {
		return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png), false, nil
	}

	pageURL := ""
	if info, infoErr := pp.Info(); infoErr == nil {
		pageURL = info.URL
	}
	return "", false, errors.Wrapf(qrErr, "qrcode not found (url=%s)", pageURL)

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
