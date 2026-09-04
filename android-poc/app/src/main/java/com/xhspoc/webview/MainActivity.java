package com.xhspoc.webview;

import android.annotation.SuppressLint;
import android.app.Activity;
import android.os.Bundle;
import android.webkit.CookieManager;
import android.webkit.WebResourceRequest;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import android.widget.Button;
import android.widget.TextView;

public class MainActivity extends Activity {
    private static final String LOGIN_URL = "https://www.xiaohongshu.com/explore";
    private static final String ATTACHMENT_URL = "https://xhslink.cn/o/6ps3iDim5IT";
    private static final String COOKIE_URL = "https://www.xiaohongshu.com";

    private WebView webView;
    private TextView status;

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        setContentView(R.layout.activity_main);
        status = findViewById(R.id.status);
        webView = findViewById(R.id.webview);
        configureWebView();

        ((Button) findViewById(R.id.login_button)).setOnClickListener(v -> openLogin());
        ((Button) findViewById(R.id.check_button)).setOnClickListener(v -> checkSession());
        ((Button) findViewById(R.id.attachment_button)).setOnClickListener(v -> openAttachment());
        checkSession();
    }

    @SuppressLint("SetJavaScriptEnabled")
    private void configureWebView() {
        CookieManager cookies = CookieManager.getInstance();
        cookies.setAcceptCookie(true);
        cookies.setAcceptThirdPartyCookies(webView, true);

        webView.getSettings().setJavaScriptEnabled(true);
        webView.getSettings().setDomStorageEnabled(true);
        webView.getSettings().setDatabaseEnabled(true);
        webView.getSettings().setUseWideViewPort(true);
        webView.getSettings().setLoadWithOverviewMode(true);
        webView.getSettings().setUserAgentString(
                "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
                        + "(KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36");
        webView.setWebViewClient(new WebViewClient() {
            @Override
            public boolean shouldOverrideUrlLoading(WebView view, WebResourceRequest request) {
                String scheme = request.getUrl().getScheme();
                return !"http".equals(scheme) && !"https".equals(scheme);
            }

            @Override
            public void onPageFinished(WebView view, String url) {
                status.setText("页面已加载：" + safeUrl(url) + "\n标题：" + view.getTitle());
            }
        });
    }

    private void openLogin() {
        status.setText("正在打开网页版。请点页面内的登录，再选择手机号验证码登录。");
        webView.loadUrl(LOGIN_URL);
    }

    private void openAttachment() {
        status.setText("正在打开附件测试帖。");
        webView.loadUrl(ATTACHMENT_URL);
    }

    private void checkSession() {
        CookieManager.getInstance().flush();
        String cookie = CookieManager.getInstance().getCookie(COOKIE_URL);
        boolean hasCookie = cookie != null && !cookie.isEmpty();
        status.setText(hasCookie
                ? "发现小红书会话 Cookie。重启 App 后再次点这里验证是否保留。"
                : "没有发现小红书会话。请先点“打开登录页”。");
    }

    private String safeUrl(String rawUrl) {
        int query = rawUrl.indexOf('?');
        return query >= 0 ? rawUrl.substring(0, query) : rawUrl;
    }

    @Override
    protected void onPause() {
        CookieManager.getInstance().flush();
        super.onPause();
    }
}
