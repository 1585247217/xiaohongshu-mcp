package com.xhsmcp.sessionpoc;

import android.annotation.SuppressLint;
import android.app.Activity;
import android.os.Bundle;
import android.webkit.CookieManager;
import android.webkit.WebResourceRequest;
import android.webkit.WebResourceResponse;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import android.widget.Button;
import android.widget.TextView;

public final class MainActivity extends Activity {
    private static final String EXPLORE = "https://www.xiaohongshu.com/explore";
    private static final String ATTACHMENT_TEST = "https://xhslink.cn/o/6ps3iDim5IT";
    private WebView webView;
    private TextView status;

    @SuppressLint("SetJavaScriptEnabled")
    @Override public void onCreate(Bundle state) {
        super.onCreate(state);
        setContentView(R.layout.activity_main);
        webView = findViewById(R.id.webview);
        status = findViewById(R.id.status);
        CookieManager cookies = CookieManager.getInstance();
        cookies.setAcceptCookie(true);
        cookies.setAcceptThirdPartyCookies(webView, true);
        webView.getSettings().setJavaScriptEnabled(true);
        webView.getSettings().setDomStorageEnabled(true);
        webView.getSettings().setUserAgentString(webView.getSettings().getUserAgentString() + " XHSSessionPoC/0.1");
        webView.setWebViewClient(new WebViewClient() {
            @Override public void onPageFinished(WebView view, String url) {
                status.setText("页面已加载： " + safeUrl(url) + "。登录后点“验证登录”。");
            }
            @Override public WebResourceResponse shouldInterceptRequest(WebView view, WebResourceRequest request) {
                String u = request.getUrl().toString().toLowerCase();
                if (u.contains(".pdf") || u.contains(".docx") || u.contains("attachment") || u.contains("download")) {
                    runOnUiThread(() -> status.setText("检测到附件候选请求（地址已隐藏）。"));
                }
                return super.shouldInterceptRequest(view, request);
            }
        });
        findViewById(R.id.verify).setOnClickListener(v -> verifySession());
        findViewById(R.id.open_attachment).setOnClickListener(v -> webView.loadUrl(ATTACHMENT_TEST));
        webView.loadUrl(EXPLORE);
    }

    private void verifySession() {
        webView.evaluateJavascript("(() => JSON.stringify({title:document.title,body:(document.body?.innerText||'').length,hasState:!!window.__INITIAL_STATE__}))()", value -> {
            boolean hasCookies = CookieManager.getInstance().hasCookies();
            status.setText("会话检查：cookies=" + hasCookies + "，页面=" + value);
            CookieManager.getInstance().flush();
        });
    }

    private static String safeUrl(String url) {
        try { return android.net.Uri.parse(url).getHost(); } catch (Exception ignored) { return "小红书"; }
    }

    @Override protected void onPause() {
        CookieManager.getInstance().flush();
        super.onPause();
    }
}
