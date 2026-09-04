package com.xhspoc.webview;

import android.annotation.SuppressLint;
import android.app.Activity;
import android.os.Bundle;
import android.webkit.CookieManager;
import android.webkit.WebResourceRequest;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import android.widget.Button;
import android.widget.EditText;
import android.widget.TextView;
import android.app.AlertDialog;
import android.content.DialogInterface;
import org.json.JSONObject;
import java.io.OutputStream;
import java.net.HttpURLConnection;
import java.net.URL;

public class MainActivity extends Activity {
    private static final String LOGIN_URL = "https://www.xiaohongshu.com/explore";
    private static final String ATTACHMENT_URL = "https://xhslink.cn/o/6ps3iDim5IT";
    private static final String COOKIE_URL = "https://www.xiaohongshu.com";
    private static final String SYNC_URL = "https://xiaohongshu-mcp-read.onrender.com/api/v1/mobile/session";
    private WebView webView;
    private TextView status;

    @Override protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        setContentView(R.layout.activity_main);
        status = findViewById(R.id.status);
        webView = findViewById(R.id.webview);
        configureWebView();
        ((Button) findViewById(R.id.login_button)).setOnClickListener(v -> openLogin());
        ((Button) findViewById(R.id.check_button)).setOnClickListener(v -> checkSession());
        ((Button) findViewById(R.id.sync_button)).setOnClickListener(v -> askSyncToken());
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
        webView.getSettings().setUserAgentString("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36");
        webView.setWebViewClient(new WebViewClient() {
            @Override public boolean shouldOverrideUrlLoading(WebView view, WebResourceRequest request) {
                String scheme = request.getUrl().getScheme();
                return !("http".equals(scheme) || "https".equals(scheme));
            }
            @Override public void onPageFinished(WebView view, String url) {
                status.setText("页面已加载：" + safeUrl(url) + "\n标题：" + view.getTitle());
            }
        });
    }

    private void openLogin() { status.setText("请在页面内完成登录。"); webView.loadUrl(LOGIN_URL); }
    private void openAttachment() { status.setText("正在打开附件测试帖。"); webView.loadUrl(ATTACHMENT_URL); }

    private void checkSession() {
        CookieManager.getInstance().flush();
        String cookie = CookieManager.getInstance().getCookie(COOKIE_URL);
        status.setText(cookie != null && !cookie.isEmpty() ? "发现会话 Cookie。" : "没有发现会话，请先登录。");
    }

    private void askSyncToken() {
        final EditText input = new EditText(this);
        input.setHint("Render 的 AUTH_TOKEN");
        input.setSingleLine(true);
        new AlertDialog.Builder(this).setTitle("同步登录态").setMessage("仅首次同步需要。同步后无需后台运行本 App。")
            .setView(input).setNegativeButton("取消", null)
            .setPositiveButton("同步", (d, w) -> syncSession(input.getText().toString().trim())).show();
    }

    private void syncSession(String token) {
        CookieManager.getInstance().flush();
        String cookie = CookieManager.getInstance().getCookie(COOKIE_URL);
        if (token.isEmpty() || cookie == null || cookie.isEmpty()) { status.setText("缺少 AUTH_TOKEN 或 Cookie。"); return; }
        status.setText("正在加密同步登录态……");
        new Thread(() -> {
            try {
                HttpURLConnection c = (HttpURLConnection) new URL(SYNC_URL).openConnection();
                c.setRequestMethod("POST"); c.setConnectTimeout(15000); c.setReadTimeout(30000);
                c.setDoOutput(true); c.setRequestProperty("Content-Type", "application/json");
                c.setRequestProperty("Authorization", "Bearer " + token);
                JSONObject body = new JSONObject(); body.put("cookie", cookie);
                try (OutputStream out = c.getOutputStream()) { out.write(body.toString().getBytes("UTF-8")); }
                int code = c.getResponseCode();
                runOnUiThread(() -> status.setText(code == 200 ? "登录态已同步。以后不用运行此 App。" : "同步失败：HTTP " + code));
                c.disconnect();
            } catch (Exception e) { runOnUiThread(() -> status.setText("同步失败：" + e.getClass().getSimpleName())); }
        }).start();
    }

    private String safeUrl(String rawUrl) { int q = rawUrl.indexOf('?'); return q >= 0 ? rawUrl.substring(0, q) : rawUrl; }
    @Override protected void onPause() { CookieManager.getInstance().flush(); super.onPause(); }
}
