package xiaohongshu

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/avast/retry-go/v4"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/errors"
	"github.com/xpzouying/xiaohongshu-mcp/humanize"
)

var attachmentURLPattern = regexp.MustCompile(`https?://[^\s"'<>\\]+`)
var attachmentHintPattern = regexp.MustCompile(`(?i)(?:\.docx?(?:[?#]|$)|\.pdf(?:[?#]|$)|\.xlsx?(?:[?#]|$)|\.pptx?(?:[?#]|$)|attachment|download|file)`)
var documentNamePattern = regexp.MustCompile(`(?i)\.(?:docx?|pdf|xlsx?|pptx?)`)

// captureAttachmentNetworkURL observes Chrome network responses while the
// attachment card is opened. XHS may only expose the signed document URL in
// an API response, leaving window.open/fetch/XHR hooks with nothing to see.
func captureAttachmentNetworkURL(page *rod.Page) (stop func(), result func() string) {
	listener, cancel := page.WithCancel()
	var mu sync.Mutex
	found := ""

	wait := listener.EachEvent(func(event *proto.NetworkResponseReceived) {
		mu.Lock()
		alreadyFound := found != ""
		mu.Unlock()
		if alreadyFound {
			return
		}

		responseURL := event.Response.URL
		mimeType := strings.ToLower(event.Response.MIMEType)
		isJSON := strings.Contains(mimeType, "json")
		if !isJSON && !directAttachmentURLPattern.MatchString(responseURL) {
			return
		}

		body, err := (&proto.NetworkGetResponseBody{RequestID: event.RequestID}).Call(listener)
		if err != nil {
			return
		}
		for _, candidate := range attachmentURLPattern.FindAllString(body.Body, -1) {
			candidate = strings.ReplaceAll(candidate, `\u0026`, "&")
			candidate = strings.ReplaceAll(candidate, `\\/`, "/")
			if !directAttachmentURLPattern.MatchString(candidate) {
				continue
			}
			mu.Lock()
			if found == "" {
				found = candidate
				logrus.Infof("已从浏览器网络响应捕获附件地址")
			}
			mu.Unlock()
			return
		}
		if isJSON && documentNamePattern.MatchString(body.Body) {
			endpoint := strings.SplitN(responseURL, "?", 2)[0]
			logrus.Infof("附件元数据响应包含文档名但未发现直链: %s", endpoint)
		}

		if directAttachmentURLPattern.MatchString(responseURL) {
			mu.Lock()
			if found == "" {
				found = responseURL
				logrus.Infof("已从浏览器网络请求捕获附件地址")
			}
			mu.Unlock()
		}
	})
	go wait()

	return cancel, func() string {
		mu.Lock()
		defer mu.Unlock()
		return found
	}
}

// ========== 配置常量 ==========
const (
	defaultMaxAttempts   = 500
	stagnantLimit        = 20
	minScrollDelta       = 10
	maxClickPerRound     = 3
	largeScrollTrigger   = 5 // 停滞多少次后触发大滚动
	buttonClickInterval  = 3 // 每隔多少次尝试点击一次按钮
	finalSprintPushCount = 15

	// 以下三个只用于查找单条评论，与批量加载的 defaultMaxAttempts 不共用
	maxSearchScrolls  = 25               // 最多下滚轮数
	maxExpandRounds   = 5                // 最多连续展开而不下滚的轮数
	maxSearchDuration = 90 * time.Second // 单次查找的墙钟上限
)

// ========== 数据结构 ==========

type CommentLoadConfig struct {
	ClickMoreReplies    bool
	MaxRepliesThreshold int
	MaxCommentItems     int
	ScrollSpeed         string
}

// 未显式指定时的默认值。
const (
	defaultMaxCommentItems     = 20
	defaultMaxRepliesThreshold = 10
	defaultScrollSpeed         = "normal"
)

func DefaultCommentLoadConfig() CommentLoadConfig {
	return CommentLoadConfig{
		ClickMoreReplies:    false,
		MaxRepliesThreshold: defaultMaxRepliesThreshold,
		MaxCommentItems:     defaultMaxCommentItems,
		ScrollSpeed:         defaultScrollSpeed,
	}
}

// normalize 把零值字段填回默认值。零值一律按「未设置」处理，不再按「无上限」。
//
// 之所以必须在这一层做：配置从 MCP 和 HTTP 两条路进来，字段名和结构都不一样，
// 漏传很容易发生。HTTP 侧要的是嵌套的 comment_config，传扁平字段会被
// ShouldBindJSON 静默丢掉，于是 MaxCommentItems=0；而 0 以前表示「无上限」，
// 一次详情请求就会滚满 defaultMaxAttempts(500) 轮。放在 action 层而不是某个
// handler 里，两条路径和以后新增的调用方都能覆盖到。
//
// 真要拉更多评论，显式传一个大的 MaxCommentItems。
func (c CommentLoadConfig) normalize() CommentLoadConfig {
	if c.MaxCommentItems <= 0 {
		c.MaxCommentItems = defaultMaxCommentItems
	}
	if c.MaxRepliesThreshold <= 0 {
		c.MaxRepliesThreshold = defaultMaxRepliesThreshold
	}
	if c.ScrollSpeed == "" {
		c.ScrollSpeed = defaultScrollSpeed
	}
	return c
}

type FeedDetailAction struct {
	page *rod.Page
}

func NewFeedDetailAction(page *rod.Page) *FeedDetailAction {
	return &FeedDetailAction{page: page}
}

// ========== 主要业务逻辑 ==========

func (f *FeedDetailAction) GetFeedDetail(ctx context.Context, feedID, xsecToken string, loadAllComments bool, config CommentLoadConfig) (*FeedDetailResponse, error) {
	return f.GetFeedDetailWithConfig(ctx, feedID, xsecToken, loadAllComments, config)
}

func (f *FeedDetailAction) GetFeedDetailWithConfig(ctx context.Context, feedID, xsecToken string, loadAllComments bool, config CommentLoadConfig) (*FeedDetailResponse, error) {
	config = config.normalize()

	page := f.page.Context(ctx).Timeout(10 * time.Minute)
	url := makeFeedDetailURL(feedID, xsecToken)

	// Start attachment response capture before navigation. The signed document
	// URL may exist only in the initial note API response and disappear before
	// the lazy attachment card reaches the DOM.
	stopAttachmentCapture, getNavigationAttachmentURL := captureAttachmentNetworkURL(page)
	defer stopAttachmentCapture()

	logrus.Infof("打开 feed 详情页: %s", url)
	logrus.Infof("配置: 点击更多=%v, 回复阈值=%d, 最大评论数=%d, 滚动速度=%s",
		config.ClickMoreReplies, config.MaxRepliesThreshold, config.MaxCommentItems, config.ScrollSpeed)

	// 使用retry-go处理页面导航和DOM稳定等待
	err := retry.Do(
		func() error {
			navigationPage := page.Timeout(8 * time.Second)
			if navigationErr := navigationPage.Navigate(url); navigationErr != nil {
				// The note is a client-rendered page and its useful state is often
				// available before every resource finishes. Continue to the explicit
				// state check instead of waiting through a redirect to an interstitial.
				logrus.Infof("详情页导航未完全结束，提前检查笔记状态: %v", navigationErr)
			}
			// Do not wait for DOM stability here. XHS continuously mutates the
			// page and may redirect the visible document while retaining note
			// state in memory. The state retry below is the readiness check, and
			// starting it immediately preserves short-lived attachment cards.
			return nil
		},
		retry.Attempts(3),
		retry.Delay(500*time.Millisecond),
		retry.MaxJitter(1000*time.Millisecond),
		retry.OnRetry(func(n uint, err error) {
			logrus.Debugf("页面导航重试 #%d: %v", n, err)
		}),
	)
	if err != nil {
		logrus.Errorf("页面导航失败: %v", err)
		return nil, err
	}
	if err := checkPageAccessible(page); err != nil {
		return nil, err
	}

	// The attachment card can exist only briefly. Inspect it before the slower
	// note-state retry loop; if its click navigates this tab, restore the note
	// URL before extracting the normal response.
	var earlyAttachment *FeedAttachment
	if navigationURL := getNavigationAttachmentURL(); navigationURL != "" {
		earlyAttachment = &FeedAttachment{Name: "小红书附件", URL: navigationURL, Source: "browser-network-navigation"}
	} else {
		attachmentPage := page.Timeout(18 * time.Second)
		if attachment, attachmentErr := captureAttachmentDownload(attachmentPage); attachmentErr != nil {
			logrus.Debugf("提前捕捉附件失败: %v", attachmentErr)
		} else {
			earlyAttachment = attachment
		}
	}
	if info, infoErr := page.Info(); infoErr == nil && !strings.Contains(info.URL, feedID) {
		restorePage := page.Timeout(8 * time.Second)
		if restoreErr := restorePage.Navigate(url); restoreErr != nil {
			logrus.Debugf("恢复笔记页未完全结束，继续读取已加载状态: %v", restoreErr)
		}
	}

	if loadAllComments {
		if err := f.loadAllCommentsWithConfig(ctx, page, config); err != nil {
			logrus.Warnf("加载全部评论失败: %v", err)
		}
	}

	// ctx 已取消时直接返回，避免在已取消的 page 上执行 MustEval 触发 panic
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return f.extractFeedDetail(page, feedID, earlyAttachment, getNavigationAttachmentURL)
}

// ========== 评论加载器 ==========

type commentLoader struct {
	page   *rod.Page
	config CommentLoadConfig
	stats  *loadStats
	state  *loadState
}

type loadStats struct {
	totalClicked int
	totalSkipped int
	attempts     int
}

type loadState struct {
	lastCount      int
	lastScrollTop  int
	stagnantChecks int
}

func (f *FeedDetailAction) loadAllCommentsWithConfig(ctx context.Context, page *rod.Page, config CommentLoadConfig) error {
	loader := &commentLoader{
		page:   page,
		config: config,
		stats:  &loadStats{},
		state:  &loadState{},
	}

	return loader.load(ctx)
}

func (cl *commentLoader) load(ctx context.Context) error {
	maxAttempts := cl.calculateMaxAttempts()

	logrus.Info("开始加载评论...")
	scrollToCommentsArea(cl.page)
	humanize.Delay(ctx, humanize.BetweenScroll)

	// 检查是否没有评论
	if cl.checkNoComments() {
		return nil
	}

	for cl.stats.attempts = 0; cl.stats.attempts < maxAttempts; cl.stats.attempts++ {
		// 协作取消点：ctx 取消后干净退出，避免空转直到撞上 MustEval panic
		if err := ctx.Err(); err != nil {
			logrus.Infof("上下文已取消，停止加载评论: %v", err)
			return err
		}

		logrus.Debugf("=== 尝试 %d/%d ===", cl.stats.attempts+1, maxAttempts)

		if cl.checkComplete(ctx) {
			return nil
		}

		if cl.shouldClickButtons() {
			cl.clickButtonsWithRetry(ctx)
		}

		currentCount := getCommentCount(cl.page)
		cl.updateState(currentCount)

		if cl.shouldStopAtTarget(currentCount) {
			return nil
		}

		cl.performScroll(ctx)
		cl.handleStagnation(ctx)

		humanize.Delay(ctx, humanize.BetweenScroll)
	}

	cl.performFinalSprint(ctx)
	return nil
}

func (cl *commentLoader) calculateMaxAttempts() int {
	if cl.config.MaxCommentItems > 0 {
		return cl.config.MaxCommentItems * 3
	}
	return defaultMaxAttempts
}

func (cl *commentLoader) checkNoComments() bool {
	if checkNoCommentsArea(cl.page) {
		logrus.Infof("✓ 检测到无评论区域（这是一片荒地），跳过加载")
		return true
	}
	return false
}

func (cl *commentLoader) checkComplete(ctx context.Context) bool {
	if !checkEndContainer(cl.page) {
		return false
	}

	// 到底之后再展开一轮：评论区不用怎么滚就能到底时，循环第一轮就走到这里，
	// 主流程里的展开步骤根本没机会执行。点开了就先不算加载完，让下一轮重新判断
	// （展开会露出新的按钮）；一个都没点开就收工，避免被阈值跳过的按钮卡住。
	if cl.config.ClickMoreReplies && cl.clickButtonsWithRetry(ctx) > 0 {
		return false
	}

	currentCount := getCommentCount(cl.page)
	logrus.Infof("✓ 检测到 'THE END' 元素，已滑动到底部")
	humanize.Delay(ctx, humanize.BetweenScroll)
	logrus.Infof("✓ 加载完成: %d 条评论, 尝试次数: %d, 点击: %d, 跳过: %d",
		currentCount, cl.stats.attempts+1, cl.stats.totalClicked, cl.stats.totalSkipped)
	return true
}

func (cl *commentLoader) shouldClickButtons() bool {
	return cl.config.ClickMoreReplies && cl.stats.attempts%buttonClickInterval == 0
}

// clickButtonsWithRetry 展开当前页上的回复按钮，返回本次点开的个数。
func (cl *commentLoader) clickButtonsWithRetry(ctx context.Context) int {
	clicked, skipped := clickShowMoreButtonsSmart(ctx, cl.page, cl.config.MaxRepliesThreshold)
	if clicked == 0 && skipped == 0 {
		return 0
	}

	cl.stats.totalClicked += clicked
	cl.stats.totalSkipped += skipped
	logrus.Infof("点击'更多': %d 个, 跳过: %d 个, 累计点击: %d, 累计跳过: %d",
		clicked, skipped, cl.stats.totalClicked, cl.stats.totalSkipped)

	humanize.Delay(ctx, humanize.Reading)

	// 重试一轮
	clicked2, skipped2 := clickShowMoreButtonsSmart(ctx, cl.page, cl.config.MaxRepliesThreshold)
	if clicked2 > 0 || skipped2 > 0 {
		cl.stats.totalClicked += clicked2
		cl.stats.totalSkipped += skipped2
		logrus.Infof("第 2 轮: 点击 %d, 跳过 %d", clicked2, skipped2)
		humanize.Delay(ctx, humanize.Reading)
	}

	return clicked + clicked2
}

func (cl *commentLoader) updateState(currentCount int) {
	totalCount := getTotalCommentCount(cl.page)
	logrus.Debugf("当前评论: %d, 目标: %d", currentCount, totalCount)

	if currentCount != cl.state.lastCount {
		logrus.Infof("✓ 评论增加: %d -> %d (+%d)",
			cl.state.lastCount, currentCount, currentCount-cl.state.lastCount)
		cl.state.lastCount = currentCount
		cl.state.stagnantChecks = 0
	} else {
		cl.state.stagnantChecks++
		if cl.state.stagnantChecks%5 == 0 {
			logrus.Debugf("评论停滞 %d 次", cl.state.stagnantChecks)
		}
	}
}

func (cl *commentLoader) shouldStopAtTarget(currentCount int) bool {
	// 如果未设置最大评论数，或者还未达到目标，继续加载
	if cl.config.MaxCommentItems <= 0 {
		return false
	}

	// 如果已达到或超过目标评论数，立即停止
	if currentCount >= cl.config.MaxCommentItems {
		logrus.Infof("✓ 已达到目标评论数: %d/%d, 停止加载",
			currentCount, cl.config.MaxCommentItems)
		return true
	}

	return false
}

func (cl *commentLoader) performScroll(ctx context.Context) {
	currentCount := getCommentCount(cl.page)
	if currentCount > 0 {
		scrollToLastComment(cl.page)
		time.Sleep(400 * time.Millisecond) // 技术 settle：等 scrollIntoView 动画落位
	}

	largeMode := cl.state.stagnantChecks >= largeScrollTrigger
	pushCount := 1
	if largeMode {
		pushCount = 3 + rand.Intn(3)
	}

	_, scrollDelta, currentScrollTop := humanScroll(ctx, cl.page, cl.config.ScrollSpeed, largeMode, pushCount)

	if scrollDelta < minScrollDelta || currentScrollTop == cl.state.lastScrollTop {
		cl.state.stagnantChecks++
		if cl.state.stagnantChecks%5 == 0 {
			logrus.Debugf("滚动停滞 %d 次", cl.state.stagnantChecks)
		}
	} else {
		cl.state.stagnantChecks = 0
		cl.state.lastScrollTop = currentScrollTop
	}
}

func (cl *commentLoader) handleStagnation(ctx context.Context) {
	if cl.state.stagnantChecks >= stagnantLimit {
		logrus.Infof("停滞过多，尝试大冲刺...")
		humanScroll(ctx, cl.page, cl.config.ScrollSpeed, true, 10)
		cl.state.stagnantChecks = 0

		if checkEndContainer(cl.page) {
			currentCount := getCommentCount(cl.page)
			logrus.Infof("✓ 到达底部，评论数: %d", currentCount)
		}
	}
}

func (cl *commentLoader) performFinalSprint(ctx context.Context) {
	logrus.Infof("达到最大尝试次数，最后冲刺...")
	humanScroll(ctx, cl.page, cl.config.ScrollSpeed, true, finalSprintPushCount)

	currentCount := getCommentCount(cl.page)
	hasEnd := checkEndContainer(cl.page)
	logrus.Infof("✓ 加载结束: %d 条评论, 点击: %d, 跳过: %d, 到达底部: %v",
		currentCount, cl.stats.totalClicked, cl.stats.totalSkipped, hasEnd)
}

// ========== 按钮点击 ==========

func clickShowMoreButtonsSmart(ctx context.Context, page *rod.Page, maxRepliesThreshold int) (clicked, skipped int) {
	elements, err := page.Elements(".show-more")
	if err != nil {
		return 0, 0
	}

	replyCountRegex := regexp.MustCompile(`展开\s*(\d+)\s*条回复`)
	maxClick := maxClickPerRound + rand.Intn(maxClickPerRound)
	clickedInRound := 0

	for _, el := range elements {
		if clickedInRound >= maxClick {
			break
		}

		if !isElementClickable(el) {
			continue
		}

		text, err := el.Text()
		if err != nil {
			continue
		}

		if !isSafeExpandButton(el, text) {
			continue
		}

		if shouldSkipButton(text, maxRepliesThreshold, replyCountRegex) {
			skipped++
			continue
		}

		if clickElementWithHumanBehavior(ctx, page, el, text) {
			clicked++
			clickedInRound++
		}
	}

	return clicked, skipped
}

// expandNearbyReplies 展开视口附近的「展开 N 条回复」，返回本轮点开的个数。
// 限定在视口附近，避免 ScrollIntoView 把页面拽回顶部、与向下滚动互相抵消。
func expandNearbyReplies(ctx context.Context, page *rod.Page) int {
	elements, err := page.Elements(".show-more")
	if err != nil || len(elements) == 0 {
		return 0
	}

	maxClick := maxClickPerRound + rand.Intn(maxClickPerRound)
	clicked := 0

	for _, el := range elements {
		if clicked >= maxClick {
			break
		}

		if !isElementClickable(el) || !isNearViewport(page, el) {
			continue
		}

		text, err := el.Text()
		if err != nil {
			continue
		}

		if !isSafeExpandButton(el, text) {
			continue
		}

		if clickElementWithHumanBehavior(ctx, page, el, text) {
			clicked++
		}
	}

	return clicked
}

// isSafeExpandButton 判断 .show-more 是不是展开回复按钮。
func isSafeExpandButton(el *rod.Element, text string) bool {
	if !isExpandRepliesButton(text) {
		logrus.Debugf("跳过展开按钮：文案不匹配 %q", text)
		return false
	}

	if !hasReadableSize(el) {
		logrus.Debugf("跳过展开按钮：尺寸过小 %q", text)
		return false
	}

	return true
}

// 两种文案：「展开 N 条回复」，以及点开一次后不带数字的「展开更多回复」。
var expandRepliesTextRegex = regexp.MustCompile(`^展开\s*(\d+\s*条|更多)回复$`)

func isExpandRepliesButton(text string) bool {
	return expandRepliesTextRegex.MatchString(strings.TrimSpace(text))
}

// hasReadableSize 判断元素尺寸是否达到按钮的量级。
func hasReadableSize(el *rod.Element) bool {
	const minWidth, minHeight = 24, 10

	shape, err := el.Shape()
	if err != nil || len(shape.Quads) == 0 {
		return false
	}

	q := shape.Quads[0] // 四个角点，左上 (q0,q1) 右下 (q4,q5)
	return q[4]-q[0] >= minWidth && q[5]-q[1] >= minHeight
}

// isNearViewport 判断元素是否落在视口上下各一屏的范围内。上下各留一屏是为了重叠，
// 滚动后刚划出去的元素下一轮还能被捡回来。
func isNearViewport(page *rod.Page, el *rod.Element) bool {
	shape, err := el.Shape()
	if err != nil || len(shape.Quads) == 0 {
		return false
	}

	// quads 是相对视口的 CSS 像素
	top := shape.Quads[0][1]
	height := float64(page.MustEval(`() => window.innerHeight`).Int())

	return top > -height && top < 2*height
}

func isElementClickable(el *rod.Element) bool {
	visible, err := el.Visible()
	if err != nil || !visible {
		return false
	}

	box, err := el.Shape()
	return err == nil && len(box.Quads) > 0
}

func shouldSkipButton(text string, threshold int, regex *regexp.Regexp) bool {
	if threshold <= 0 {
		return false
	}

	matches := regex.FindStringSubmatch(text)
	if len(matches) > 1 {
		if replyCount, err := strconv.Atoi(matches[1]); err == nil && replyCount > threshold {
			logrus.Debugf("跳过'%s'（回复数 %d > 阈值 %d）", text, replyCount, threshold)
			return true
		}
	}
	return false
}

func clickElementWithHumanBehavior(ctx context.Context, page *rod.Page, el *rod.Element, text string) bool {
	var clickSuccess bool

	// 使用retry-go进行点击操作重试
	err := retry.Do(
		func() error {
			// 滚动到元素
			if err := el.ScrollIntoView(); err != nil {
				return err
			}

			humanize.Delay(ctx, humanize.Reading)

			// 点击（humanize.Click 自己取落点并移动过去）
			if err := humanize.Click(el); err != nil {
				return err // 返回错误以触发重试
			}

			humanize.Delay(ctx, humanize.Reading)
			clickSuccess = true
			return nil
		},
		retry.Attempts(3),
		retry.Delay(100*time.Millisecond),
		retry.MaxJitter(200*time.Millisecond),
		retry.OnRetry(func(n uint, err error) {
			logrus.Debugf("点击重试 #%d: %s, 错误: %v", n, text, err)
		}),
	)

	if err != nil {
		logrus.Debugf("点击失败 '%s': %v", text, err)
		return false
	}

	if clickSuccess {
		logrus.Debugf("点击了'%s'", text)
	}

	return clickSuccess
}

// ========== 滚动相关 ==========

func humanScroll(ctx context.Context, page *rod.Page, speed string, largeMode bool, pushCount int) (bool, int, int) {
	beforeTop := getScrollTop(page)
	viewportHeight := page.MustEval(`() => window.innerHeight`).Int()

	baseRatio := getScrollRatio(speed)
	if largeMode {
		baseRatio *= 2.0
	}

	scrolled := false
	actualDelta := 0
	currentScrollTop := beforeTop

	for i := 0; i < max(1, pushCount); i++ {
		scrollDelta := calculateScrollDelta(viewportHeight, baseRatio)
		smartScroll(page, scrollDelta)

		time.Sleep(150 * time.Millisecond) // 技术 settle：等滚动后懒加载渲染，再读 scrollTop

		currentScrollTop = getScrollTop(page)
		deltaThisTime := currentScrollTop - beforeTop
		actualDelta += deltaThisTime

		if deltaThisTime > 5 {
			scrolled = true
		}

		beforeTop = currentScrollTop

		if i < pushCount-1 {
			humanize.Delay(ctx, humanize.BetweenScroll)
		}
	}

	// 兜底：常规幅度没推动，加大力度再滚一次。
	// 不用 window.scrollTo：详情页评论在容器内滚动，window 的 scrollTop 恒为 0
	// （见 getScrollTop）。实测滚 window 推不动评论容器，读回来的位移也不是它的。
	if !scrolled && pushCount > 0 {
		smartScroll(page, float64(viewportHeight)*3)
		time.Sleep(400 * time.Millisecond) // 技术 settle：等滚动落位
		currentScrollTop = getScrollTop(page)
		actualDelta += currentScrollTop - beforeTop
		scrolled = actualDelta > 5
	}

	if scrolled {
		logrus.Debugf("滚动: %d -> %d (Δ%d, large=%v, push=%d)",
			beforeTop-actualDelta, currentScrollTop, actualDelta, largeMode, pushCount)
	}

	return scrolled, actualDelta, currentScrollTop
}

func getScrollRatio(speed string) float64 {
	switch speed {
	case "slow":
		return 0.5
	case "fast":
		return 0.9
	default: // normal
		return 0.7
	}
}

func calculateScrollDelta(viewportHeight int, baseRatio float64) float64 {
	scrollDelta := float64(viewportHeight) * (baseRatio + rand.Float64()*0.2)
	if scrollDelta < 400 {
		scrollDelta = 400
	}
	return scrollDelta + float64(rand.Intn(100)-50)
}

func scrollToCommentsArea(page *rod.Page) {
	logrus.Info("滚动到评论区...")

	// 先定位到评论区
	if el, err := page.Timeout(2 * time.Second).Element(".comments-container"); err == nil {
		el.MustScrollIntoView()
	}
	// 等 scrollIntoView 动画落位
	time.Sleep(400 * time.Millisecond)

	// 触发一次小滚动，激活懒加载机制
	smartScroll(page, 100)
}

// smartScroll 向下滚动 delta 像素，触发评论区懒加载。
// 按滚轮格逐格发送，每格幅度小幅浮动、格间留间隔。
func smartScroll(page *rod.Page, delta float64) {
	// 指针落在评论滚动容器上，滚轮才只作用于评论区（否则会滚整页）
	moveToCommentScroller(page)

	for remain := delta; remain > 0; {
		notch := scrollNotchSize()
		if notch > remain {
			notch = remain
		}

		if err := page.Mouse.Scroll(0, notch, 1); err != nil {
			return
		}
		remain -= notch

		if remain > 0 {
			time.Sleep(scrollNotchInterval())
		}
	}
}

// scrollNotchSize 单格滚轮的幅度，围绕标准的 120px 浮动。
func scrollNotchSize() float64 {
	return 100 + rand.Float64()*40
}

// scrollNotchInterval 连续滚轮格之间的间隔。
func scrollNotchInterval() time.Duration {
	return time.Duration(20+rand.Intn(45)) * time.Millisecond
}

// commentScrollerSelectors 评论区滚动容器，按优先级排列。
// 滚动与测量位移必须指向同一个容器，因此共用这一份定义。
var commentScrollerSelectors = []string{".note-scroller", ".comments-container"}

// moveToCommentScroller 把指针移到评论滚动容器内；找不到则退回视口中心。
// 指针已在容器内时不再移动，避免重复落到同一点。
func moveToCommentScroller(page *rod.Page) {
	for _, sel := range commentScrollerSelectors {
		el, err := page.Timeout(2 * time.Second).Element(sel)
		if err != nil {
			continue
		}
		shape, err := el.Shape()
		if err != nil || len(shape.Quads) == 0 {
			continue
		}
		q := shape.Quads[0]
		left, top, right, bottom := q[0], q[1], q[4], q[5]

		if pos := page.Mouse.Position(); pos.X > left && pos.X < right && pos.Y > top && pos.Y < bottom {
			return
		}

		// 落点在容器中心附近随机偏移，不固定在几何中心
		cx, cy := (left+right)/2, (top+bottom)/2
		_ = humanize.MoveTo(page, proto.Point{
			X: cx + (rand.Float64()-0.5)*(right-left)*0.3,
			Y: cy + (rand.Float64()-0.5)*(bottom-top)*0.3,
		})
		return
	}
	vw := page.MustEval(`() => window.innerWidth`).Int()
	vh := page.MustEval(`() => window.innerHeight`).Int()
	_ = humanize.MoveTo(page, proto.Point{X: float64(vw) / 2, Y: float64(vh) / 2})
}

func scrollToLastComment(page *rod.Page) {
	// 获取所有主评论元素
	elements, err := page.Timeout(2 * time.Second).Elements(".parent-comment")
	if err != nil || len(elements) == 0 {
		return
	}
	// 滚动到最后一个评论
	lastComment := elements[len(elements)-1]
	lastComment.MustScrollIntoView()
}

// ========== DOM 查询 ==========

func getScrollTop(page *rod.Page) int {
	var result int

	// 使用retry-go来处理可能的DOM查询失败
	err := retry.Do(
		func() error {
			evalResult := page.MustEval(`(sels) => {
				// 详情页的评论是在容器内滚动的，window 的 scrollTop 恒为 0，
				// 必须读实际滚动的那个容器；容器不可滚时才退回 window。
				for (const sel of sels) {
					const el = document.querySelector(sel);
					if (el && el.scrollHeight > el.clientHeight) {
						return el.scrollTop;
					}
				}
				return window.pageYOffset || document.documentElement.scrollTop || document.body.scrollTop || 0;
			}`, commentScrollerSelectors)

			result = evalResult.Int()
			return nil
		},
		retry.Attempts(3),
		retry.Delay(100*time.Millisecond),
		retry.MaxJitter(200*time.Millisecond),
		retry.OnRetry(func(n uint, err error) {
			logrus.Debugf("获取滚动位置重试 #%d: %v", n, err)
		}),
	)

	if err != nil {
		logrus.Warnf("获取滚动位置失败: %v", err)
		return 0 // 失败时返回0
	}

	return result
}

func getCommentCount(page *rod.Page) int {
	var result int

	// 使用retry-go来处理可能的DOM查询失败
	err := retry.Do(
		func() error {
			// 使用 Go 获取评论元素
			elements, err := page.Timeout(2 * time.Second).Elements(".parent-comment")
			if err != nil {
				return err
			}
			result = len(elements)
			return nil
		},
		retry.Attempts(3),
		retry.Delay(100*time.Millisecond),
		retry.MaxJitter(200*time.Millisecond),
		retry.OnRetry(func(n uint, err error) {
			logrus.Debugf("获取评论计数重试 #%d: %v", n, err)
		}),
	)

	if err != nil {
		logrus.Warnf("获取评论计数失败: %v", err)
		return 0 // 失败时返回0
	}

	return result
}

// getTotalCommentCount 取笔记的评论总数，读 __INITIAL_STATE__ 里的
// interactInfo.commentCount，不依赖评论区文案。取不到返回 0。
func getTotalCommentCount(page *rod.Page) int {
	res, err := page.Eval(`() => {
		const m = window.__INITIAL_STATE__?.note?.noteDetailMap;
		if (!m) return "";
		for (const v of Object.values(m)) {
			const c = v?.note?.interactInfo?.commentCount;
			if (c !== undefined && c !== null) return String(c);
		}
		return "";
	}`)
	if err != nil {
		logrus.Debugf("获取总评论计数失败: %v", err)
		return 0
	}

	count, err := strconv.Atoi(strings.TrimSpace(res.Value.Str()))
	if err != nil {
		return 0
	}
	return count
}

func checkNoCommentsArea(page *rod.Page) bool {
	// 查找无评论区域
	noCommentsEl, err := page.Timeout(2 * time.Second).Element(".no-comments-text")
	if err != nil {
		// 未找到无评论元素，说明有评论或评论区正常
		return false
	}

	// 获取文本内容
	text, err := noCommentsEl.Text()
	if err != nil {
		return false
	}

	// 检查是否包含"这是一片荒地"等关键词
	text = strings.TrimSpace(text)
	return strings.Contains(text, "这是一片荒地")
}

func checkEndContainer(page *rod.Page) bool {
	var result bool

	// 使用retry-go来处理可能的DOM查询失败
	err := retry.Do(
		func() error {
			// 使用 Go 查找结束容器
			endEl, err := page.Timeout(2 * time.Second).Element(".end-container")
			if err != nil {
				// 未找到元素，说明未到底部
				result = false
				return nil
			}

			// 获取文本内容
			text, err := endEl.Text()
			if err != nil {
				result = false
				return nil
			}

			// 转换为大写并检查
			textUpper := strings.ToUpper(strings.TrimSpace(text))
			result = strings.Contains(textUpper, "THE END") || strings.Contains(textUpper, "THEEND")
			return nil
		},
		retry.Attempts(3),
		retry.Delay(100*time.Millisecond),
		retry.MaxJitter(200*time.Millisecond),
		retry.OnRetry(func(n uint, err error) {
			logrus.Debugf("检查结束容器重试 #%d: %v", n, err)
		}),
	)

	if err != nil {
		logrus.Warnf("检查结束容器失败: %v", err)
		return false // 失败时返回false
	}

	return result
}

// ========== 页面检查 ==========

func checkPageAccessible(page *rod.Page) error {
	// 等错误提示 UI 渲染出来再检查
	time.Sleep(500 * time.Millisecond)

	// 查找错误提示容器
	wrapperEl, err := page.Timeout(2 * time.Second).Element(".access-wrapper, .error-wrapper, .not-found-wrapper, .blocked-wrapper")
	if err != nil {
		// 未找到错误容器，说明页面可访问
		return nil
	}

	// 获取文本内容
	text, err := wrapperEl.Text()
	if err != nil {
		// 无法获取文本，假设页面可访问
		return nil
	}

	// 检查关键词
	keywords := []string{
		"当前笔记暂时无法浏览",
		"该内容因违规已被删除",
		"该笔记已被删除",
		"内容不存在",
		"笔记不存在",
		"已失效",
		"私密笔记",
		"仅作者可见",
		"因用户设置，你无法查看",
		"因违规无法查看",
	}

	for _, kw := range keywords {
		if strings.Contains(text, kw) {
			logrus.Warnf("笔记不可访问: %s", kw)
			return fmt.Errorf("笔记不可访问: %s", kw)
		}
	}

	// 如果有文本但不匹配关键词，返回未知错误
	trimmedText := strings.TrimSpace(text)
	if trimmedText != "" {
		logrus.Warnf("笔记不可访问（未知原因）: %s", trimmedText)
		return fmt.Errorf("笔记不可访问: %s", trimmedText)
	}

	return nil
}

// ========== 数据提取 ==========

func (f *FeedDetailAction) extractFeedDetail(page *rod.Page, feedID string, earlyAttachment *FeedAttachment, getNavigationAttachmentURL func() string) (*FeedDetailResponse, error) {
	var result string

	// 使用retry-go来处理可能的DOM查询失败
	err := retry.Do(
		func() error {
			evalResult, evalErr := page.Eval(`() => {
				if (window.__INITIAL_STATE__ &&
					window.__INITIAL_STATE__.note &&
					window.__INITIAL_STATE__.note.noteDetailMap) {
					const noteDetailMap = window.__INITIAL_STATE__.note.noteDetailMap;
					const usable = Object.values(noteDetailMap).some(item => {
						const note = item && (item.note || item);
						return note && (note.noteId || note.id || note.title || note.desc);
					});
					if (usable) return JSON.stringify(noteDetailMap);
				}
				return "";
			}`)
			if evalErr != nil {
				return fmt.Errorf("读取初始状态时页面发生变化: %w", evalErr)
			}
			evalText := evalResult.Value.String()

			if evalText != "" {
				result = evalText
				return nil
			}
			return fmt.Errorf("无法获取初始状态数据")
		},
		retry.Attempts(8),
		retry.Delay(900*time.Millisecond),
		retry.MaxJitter(400*time.Millisecond),
		retry.OnRetry(func(n uint, err error) {
			logrus.Debugf("提取Feed详情重试 #%d: %v", n, err)
		}),
	)

	if err != nil {
		logrus.Errorf("提取Feed详情失败: %v", err)
		return nil, fmt.Errorf("提取Feed详情失败: %w", err)
	}

	if result == "" {
		return nil, errors.ErrNoFeedDetail
	}
	// Once the usable note state is present, keep the current document in place.
	// XHS may otherwise redirect the headless tab to an empty interstitial while
	// we are locating its lazy attachment card. This stops only the in-flight
	// navigation; page JavaScript and explicit attachment clicks remain active.
	if stopErr := (&proto.PageStopLoading{}).Call(page); stopErr != nil {
		logrus.Debugf("停止详情页后续导航失败: %v", stopErr)
	}

	var noteDetailMap map[string]struct {
		Note     FeedDetail  `json:"note"`
		Comments CommentList `json:"comments"`
	}

	if err := json.Unmarshal([]byte(result), &noteDetailMap); err != nil {
		return nil, fmt.Errorf("failed to unmarshal noteDetailMap: %w", err)
	}

	noteDetail, exists := noteDetailMap[feedID]
	if !exists {
		// XHS sometimes keys the detail map with an internal/cache identifier
		// instead of the feed id present in the share URL. A detail page contains
		// exactly one note, so the sole entry is unambiguous and safe to use.
		if len(noteDetailMap) != 1 {
			return nil, fmt.Errorf("feed %s not found in noteDetailMap", feedID)
		}
		for mapKey, onlyDetail := range noteDetailMap {
			if onlyDetail.Note.NoteID == "" && onlyDetail.Note.Title == "" && onlyDetail.Note.Desc == "" {
				return nil, fmt.Errorf("feed %s not found in noteDetailMap (sole entry is empty)", feedID)
			}
			noteDetail = onlyDetail
			logrus.Infof("noteDetailMap 键与分享 ID 不同，使用页面唯一笔记: %s", mapKey)
		}
	}
	// Some posts expose documents only as interactive attachment cards. Capture
	// that one explicit download before scanning passive DOM attributes: clicking
	// while scanning can consume the browser download event and leave us without
	// its signed URL.
	capturedAttachment := earlyAttachment
	if capturedAttachment == nil {
		if navigationURL := getNavigationAttachmentURL(); navigationURL != "" {
			capturedAttachment = &FeedAttachment{
				Name:   "小红书附件",
				URL:    navigationURL,
				Source: "browser-network-navigation",
			}
			logrus.Infof("使用页面首轮网络响应中的附件地址")
		}
	}
	// Cards that already expose a direct URL are absent from noteDetailMap, so
	// inspect their rendered anchors and data attributes as a second source.
	if attachments, err := extractRenderedAttachments(page); err != nil {
		logrus.Debugf("提取页面附件失败: %v", err)
	} else {
		noteDetail.Note.Attachments = attachments
	}
	if capturedAttachment != nil {
		found := false
		for _, attachment := range noteDetail.Note.Attachments {
			if attachment.URL == capturedAttachment.URL {
				found = true
				break
			}
		}
		if !found {
			noteDetail.Note.Attachments = append(noteDetail.Note.Attachments, *capturedAttachment)
		}
	}

	return &FeedDetailResponse{
		Note:     noteDetail.Note,
		Comments: noteDetail.Comments,
	}, nil
}

func extractRenderedAttachments(page *rod.Page) ([]FeedAttachment, error) {
	rawResult, evalErr := page.Eval(`async () => {
		const keys = ['href', 'data-url', 'data-download-url', 'data-file-url', 'data-src'];
		const out = [];
		const add = (name, url, source) => {
			if (!url) return;
			try { url = new URL(url, location.href).href; } catch (_) { return; }
			const hint = (name + ' ' + url).toLowerCase();
			if (/(docx?|pdf|xlsx?|pptx?|附件|文件|download|attachment)/.test(hint)) out.push({name, url, source});
		};
		for (const el of document.querySelectorAll('a, [data-url], [data-download-url], [data-file-url], [data-src]')) {
			if (el.closest('footer, .footer, [class*=footer]')) continue;
			const name = (el.getAttribute('title') || el.getAttribute('aria-label') || el.textContent || '').trim();
			for (const key of keys) {
				const value = el.getAttribute(key);
				add(name, value, key);
			}
		}
		for (const url of document.documentElement.innerHTML.match(/https?:\/\/[^\s"'<>]+/g) || []) add('', url, 'html');
		return JSON.stringify(out);
	}`)
	if evalErr != nil {
		return nil, evalErr
	}
	raw := rawResult.Value.String()
	var candidates []FeedAttachment
	if err := json.Unmarshal([]byte(raw), &candidates); err != nil { return nil, err }
	seen := make(map[string]bool)
	attachments := make([]FeedAttachment, 0, len(candidates))
	for _, item := range candidates {
		if item.URL == "" || seen[item.URL] || isPlatformFooterDocument(item) { continue }
		seen[item.URL] = true
		attachments = append(attachments, item)
	}
	return attachments, nil
}

func isPlatformFooterDocument(item FeedAttachment) bool {
	name := strings.TrimSpace(item.Name)
	return strings.HasPrefix(name, "小红书_医疗器械") ||
		strings.HasPrefix(name, "小红书_互联网药品") ||
		strings.HasPrefix(name, "小红书_沪公网安备")
}

// captureAttachmentDownload handles cards whose real URL appears only after
// a click. The listener is registered before clicking.
func captureAttachmentDownload(page *rod.Page) (*FeedAttachment, error) {
	// Start before scrolling: some XHS builds fetch attachment metadata while
	// lazily rendering the card, before it becomes clickable in the DOM.
	stopNetworkCapture, getNetworkURL := captureAttachmentNetworkURL(page)
	defer stopNetworkCapture()
	candidatePage := page

	var name string
	nameErr := retry.Do(func() error {
		nameResult, evalErr := page.Eval(`async () => {
		// Attachment cards are lazy-rendered in the note's scroll container.
		// Move likely content panes through their range before looking for the
		// card, without scrolling the comments hundreds of times.
		const panes = [...document.querySelectorAll('main, article, section, div')]
			.filter(el => el.scrollHeight > el.clientHeight + 120 && el.clientHeight > 180)
			.sort((a, b) => (b.clientHeight * b.clientWidth) - (a.clientHeight * a.clientWidth))
			.slice(0, 4);
		for (const pane of panes) {
			const before = pane.scrollTop;
			pane.scrollTop = Math.min(pane.scrollHeight, Math.max(before, pane.scrollHeight * 0.6));
		}
		window.scrollTo(0, Math.max(window.scrollY, document.documentElement.scrollHeight * 0.55));
		await new Promise(resolve => setTimeout(resolve, 1500));
		const blocked = /(医疗器械网络交易服务|互联网药品信息服务|沪公网安备)/;
		const strongHint = /\.(docx?|pdf|xlsx?|pptx?)(?:\b|$)/i;
		const weakHint = /(附件|下载)/i;
		const candidates = [];
		const roots = [document];
		for (const frame of document.querySelectorAll('iframe')) {
			try { if (frame.contentDocument) roots.push(frame.contentDocument); } catch (_) {}
		}
		for (const root of roots) for (const el of root.querySelectorAll('body *')) {
			const text = (el.getAttribute('title') || el.getAttribute('aria-label') || el.textContent || '').trim();
			if (!text || text.length > 200 || blocked.test(text)) continue;
			const attrs = [el.getAttribute('href'), el.getAttribute('data-url'), el.getAttribute('data-download-url'), el.getAttribute('data-file-url'), el.className].filter(v => typeof v === 'string').join(' ');
			const explicit = strongHint.test(text + ' ' + attrs);
			const cardLike = weakHint.test(text) && !!el.querySelector('img, svg, [class*=icon], [class*=file], [class*=attach]');
			if (!explicit && !cardLike) continue;
			const control = el.closest('a, button, [role=button], [class*=file], [class*=attach]') || el;
			let score = explicit ? 100 : 20;
			if (control.matches('a, button, [role=button]')) score += 50;
			if (/(file|attach|download|document)/i.test(String(control.className))) score += 40;
			if (control.querySelector('img, svg, [class*=icon]')) score += 20;
			// Prefer the compact file card over a paragraph or a large note wrapper.
			score -= Math.min(40, control.querySelectorAll('*').length);
			candidates.push({control, text, score});
		}
		candidates.sort((a, b) => b.score - a.score);
		if (!candidates.length) return '';
		candidates[0].control.setAttribute('data-xhs-attachment-candidate', '1');
		return candidates[0].text;
		}`)
		if evalErr != nil {
			return fmt.Errorf("识别附件卡时页面发生变化: %w", evalErr)
		}
		name = nameResult.Value.String()
		return nil
	}, retry.Attempts(2), retry.Delay(500*time.Millisecond))
	if nameErr != nil {
		return nil, nameErr
	}
	if name == "" {
		if frameElements, frameErr := page.Elements("iframe"); frameErr == nil {
			for _, frameElement := range frameElements {
				framePage, frameErr := frameElement.Frame()
				if frameErr != nil {
					continue
				}
				frameResult, frameEvalErr := framePage.Eval(`() => {
					const candidates = [...document.querySelectorAll('body *')].filter(el => {
						const text = (el.getAttribute('title') || el.getAttribute('aria-label') || el.textContent || '').trim();
						return text.length > 0 && text.length <= 200 && /\.(docx?|pdf|xlsx?|pptx?)(?:\b|$)/i.test(text);
					});
					if (!candidates.length) return '';
					const control = candidates[0].closest('a, button, [role=button], [class*=file], [class*=attach]') || candidates[0];
					control.setAttribute('data-xhs-attachment-candidate', '1');
					return (candidates[0].getAttribute('title') || candidates[0].getAttribute('aria-label') || candidates[0].textContent || '').trim();
				}`)
				if frameEvalErr == nil && frameResult.Value.String() != "" {
					name = frameResult.Value.String()
					candidatePage = framePage
					logrus.Infof("已在跨域 frame 中识别附件卡")
					break
				}
			}
		}
	}
	if name == "" {
		if networkURL := getNetworkURL(); networkURL != "" {
			return &FeedAttachment{Name: "小红书附件", URL: networkURL, Source: "browser-network-render"}, nil
		}
		diagnosticResult, diagnosticErr := page.Eval(`() => JSON.stringify({
			mentionsDoc: /(?:\.docx?|\.pdf|附件|下载)/i.test(document.body?.innerText || ''),
			frames: document.querySelectorAll('iframe').length,
			shadowRoots: [...document.querySelectorAll('*')].filter(el => !!el.shadowRoot).length
		})`)
		if diagnosticErr == nil {
			logrus.Infof("附件候选未渲染: %s", diagnosticResult.Value.String())
		} else {
			logrus.Infof("附件候选未渲染，页面已导航，跳过诊断: %v", diagnosticErr)
		}
		return nil, nil
	}
	logrus.Infof("尝试打开附件卡: %s", name)

	// File cards often open a signed document page instead of triggering a
	// browser download. Capture explicit links, window.open calls and a same-tab
	// navigation from the one intentional click before falling back to download
	// monitoring below.
	openWatcher := page.Timeout(3 * time.Second)
	waitOpen := openWatcher.WaitOpen()
	openedResult, openedErr := candidatePage.Eval(`async () => {
		const roots = [document];
		for (const frame of document.querySelectorAll('iframe')) {
			try { if (frame.contentDocument) roots.push(frame.contentDocument); } catch (_) {}
		}
		const el = roots.map(root => root.querySelector('[data-xhs-attachment-candidate="1"]')).find(Boolean);
		if (!el) return '';
		const direct = [el.href, el.getAttribute('href'), el.getAttribute('data-url'),
			el.getAttribute('data-download-url'), el.getAttribute('data-file-url')]
			.find(value => typeof value === 'string' && /^https?:/i.test(value));
		if (direct) return new URL(direct, location.href).href;
		const before = location.href;
		const opened = [];
		const responseURLs = [];
		const recordResponse = (url, body) => {
			const text = String(body || '');
			if (!/(docx?|pdf|xlsx?|pptx?|download|attachment|file)/i.test(url + ' ' + text)) return;
			const visit = value => {
				if (typeof value === 'string') {
					if (value.startsWith('http://') || value.startsWith('https://')) responseURLs.push(value);
					return;
				}
				if (Array.isArray(value)) { for (const item of value) visit(item); return; }
				if (value && typeof value === 'object') { for (const item of Object.values(value)) visit(item); }
			};
			try { visit(JSON.parse(text)); } catch (_) {}
		};
		const originalOpen = window.open;
		const originalFetch = window.fetch;
		const originalXHROpen = XMLHttpRequest.prototype.open;
		const originalXHRSend = XMLHttpRequest.prototype.send;
		window.open = function(url, ...args) {
			if (typeof url === 'string') opened.push(url);
			return originalOpen.call(this, url, ...args);
		};
		window.fetch = async function(...args) {
			const response = await originalFetch.apply(this, args);
			try { recordResponse(response.url, await response.clone().text()); } catch (_) {}
			return response;
		};
		XMLHttpRequest.prototype.open = function(method, url, ...args) {
			this.__xhsAttachmentURL = url;
			return originalXHROpen.call(this, method, url, ...args);
		};
		XMLHttpRequest.prototype.send = function(...args) {
			this.addEventListener('load', () => { try { recordResponse(this.responseURL || this.__xhsAttachmentURL, this.responseText); } catch (_) {} });
			return originalXHRSend.apply(this, args);
		};
		try { el.click(); } catch (_) {}
		await new Promise(resolve => setTimeout(resolve, 1200));
		// Some builds open an in-page document preview first. Only interact with
		// an explicit attachment action inside the newly visible dialog/panel.
		const panels = [...document.querySelectorAll('[role=dialog], [class*=modal], [class*=dialog], [class*=preview]')]
			.filter(node => node !== el && node.getBoundingClientRect().width > 0 && node.getBoundingClientRect().height > 0);
		for (const panel of panels) {
			const action = [...panel.querySelectorAll('a, button, [role=button]')].find(node =>
				/^(下载|打开|查看附件|下载附件|打开附件)$/i.test((node.textContent || node.getAttribute('aria-label') || '').trim()));
			if (!action) continue;
			const actionURL = [action.href, action.getAttribute('href'), action.getAttribute('data-url'),
				action.getAttribute('data-download-url'), action.getAttribute('data-file-url')]
				.find(value => typeof value === 'string' && /^https?:/i.test(value));
			if (actionURL) responseURLs.push(new URL(actionURL, before).href);
			try { action.click(); } catch (_) {}
			break;
		}
		await new Promise(resolve => setTimeout(resolve, 1800));
		window.open = originalOpen;
		window.fetch = originalFetch;
		XMLHttpRequest.prototype.open = originalXHROpen;
		XMLHttpRequest.prototype.send = originalXHRSend;
		const next = opened.find(value => /^https?:/i.test(value));
		if (next) return new URL(next, before).href;
		const responseURL = responseURLs.find(value => /(?:docx?|pdf|xlsx?|pptx?|download|attachment|file)/i.test(value));
		if (responseURL) return new URL(responseURL, before).href;
		const resource = performance.getEntriesByType('resource')
			.map(item => item.name)
			.reverse()
			.find(value => /(?:docx?|pdf|xlsx?|pptx?|download|attachment|file)/i.test(value));
		if (resource) return resource;
		if (location.href !== before) return location.href;
		const visiblePanels = [...document.querySelectorAll('[role=dialog], [class*=modal], [class*=dialog], [class*=preview]')]
			.filter(node => node.getBoundingClientRect().width > 0 && node.getBoundingClientRect().height > 0);
		const labels = visiblePanels.flatMap(panel => [...panel.querySelectorAll('a, button, [role=button]')]
			.map(node => (node.textContent || node.getAttribute('aria-label') || '').trim())
			.filter(Boolean)).slice(0, 12);
		return '__XHS_ATTACHMENT_DIAG__' + JSON.stringify({panels: visiblePanels.length, actions: labels});
	}`)
	openedURL := ""
	if openedErr != nil {
		logrus.Infof("附件点击触发页面导航，继续读取浏览器级捕获结果: %v", openedErr)
	} else {
		openedURL = openedResult.Value.String()
		if strings.HasPrefix(openedURL, "__XHS_ATTACHMENT_DIAG__") {
			logrus.Infof("附件预览结构: %s", strings.TrimPrefix(openedURL, "__XHS_ATTACHMENT_DIAG__"))
			openedURL = ""
		}
	}
	if networkURL := getNetworkURL(); networkURL != "" {
		return &FeedAttachment{Name: name, URL: networkURL, Source: "browser-network"}, nil
	}
	if openedPage, openErr := waitOpen(); openErr == nil && openedPage != nil {
		if info, infoErr := openedPage.Info(); infoErr == nil && info.URL != "" && info.URL != "about:blank" {
			return &FeedAttachment{Name: name, URL: info.URL, Source: "browser-new-page"}, nil
		}
	}
	if openedURL != "" {
		return &FeedAttachment{Name: name, URL: openedURL, Source: "browser-open"}, nil
	}
	logrus.Infof("附件卡未暴露可读取地址: %s", name)

	dir, err := os.MkdirTemp("", "xhs-attachment-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	waitDownload := page.Browser().Context(waitCtx).WaitDownload(dir)
	clickedResult, clickErr := candidatePage.Eval(`() => {
		const roots = [document];
		for (const frame of document.querySelectorAll('iframe')) {
			try { if (frame.contentDocument) roots.push(frame.contentDocument); } catch (_) {}
		}
		const el = roots.map(root => root.querySelector('[data-xhs-attachment-candidate="1"]')).find(Boolean);
		if (!el) return false;
		el.click();
		return true;
	}`)
	if clickErr != nil {
		return nil, clickErr
	}
	clicked := clickedResult.Value.Bool()
	if !clicked {
		return nil, nil
	}
	info := waitDownload()
	if info == nil || info.URL == "" {
		return nil, nil
	}
	return &FeedAttachment{Name: name, URL: info.URL, Source: "browser-download"}, nil
}

func makeFeedDetailURL(feedID, xsecToken string) string {
	return fmt.Sprintf("https://www.xiaohongshu.com/explore/%s?xsec_token=%s&xsec_source=pc_feed", feedID, xsecToken)
}
