package xiaohongshu

import (
    "context"
    "time"

    "github.com/go-rod/rod"
    "github.com/xpzouying/xiaohongshu-mcp/humanize"
)

type NavigateAction struct { page *rod.Page }

func NewNavigate(page *rod.Page) *NavigateAction { return &NavigateAction{page: page} }

func (n *NavigateAction) ToExplorePage(ctx context.Context) error {
    page := n.page.Context(ctx).Timeout(25 * time.Second)
    if err := page.Navigate("https://www.xiaohongshu.com/explore"); err != nil { return err }
    if err := page.WaitLoad(); err != nil { return err }
    if _, err := page.Element(`div#app`); err != nil { return err }
    return nil
}

func (n *NavigateAction) ToProfilePage(ctx context.Context) error {
    page := n.page.Context(ctx).Timeout(25 * time.Second)
    if err := n.ToExplorePage(ctx); err != nil { return err }
    if err := page.WaitStable(500 * time.Millisecond); err != nil { return err }
    profileLink, err := page.Element(`div.main-container li.user.side-bar-component a.link-wrapper span.channel`)
    if err != nil { return err }
    humanize.Delay(ctx, humanize.BeforeClick)
    if err := humanize.Click(profileLink); err != nil { return err }
    // XHS keeps long-lived requests open on profile pages. Waiting for the
    // browser load event therefore turns a successful click into a timeout.
    // The caller waits for the actual page state it needs instead.
    return nil
}
