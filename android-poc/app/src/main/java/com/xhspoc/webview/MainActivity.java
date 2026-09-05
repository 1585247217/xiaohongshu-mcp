package com.xhspoc.webview;

import android.annotation.SuppressLint;
import android.app.Activity;
import android.app.AlertDialog;
import android.content.Intent;
import android.os.Bundle;
import android.os.Build;
import android.Manifest;
import android.webkit.CookieManager;
import android.webkit.WebResourceRequest;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import android.widget.Button;
import android.widget.EditText;
import android.widget.TextView;

public class MainActivity extends Activity {
    private static final String LOGIN_URL = "https://www.xiaohongshu.com/explore";
    private static final String ATTACHMENT_URL = "https://xhslink.cn/o/6ps3iDim5IT";
    private static final String COOKIE_URL = "https://www.xiaohongshu.com";
    private WebView webView; private TextView status;

    @Override public void onCreate(Bundle state) {
        super.onCreate(state); setContentView(R.layout.activity_main);
        if (Build.VERSION.SDK_INT >= 33) requestPermissions(new String[]{Manifest.permission.POST_NOTIFICATIONS}, 7);
        status=findViewById(R.id.status); webView=findViewById(R.id.webview); configureWebView();
        ((Button)findViewById(R.id.login_button)).setOnClickListener(v->openLogin());
        ((Button)findViewById(R.id.check_button)).setOnClickListener(v->checkSession());
        ((Button)findViewById(R.id.attachment_button)).setOnClickListener(v->webView.loadUrl(ATTACHMENT_URL));
        ((Button)findViewById(R.id.agent_button)).setOnClickListener(v->askStartAgent());
        ((Button)findViewById(R.id.stop_agent_button)).setOnClickListener(v->{ stopService(new Intent(this,AgentService.class)); status.setText("读取代理已停止。"); });
        checkSession();
    }
    @SuppressLint("SetJavaScriptEnabled") private void configureWebView() {
        CookieManager c=CookieManager.getInstance(); c.setAcceptCookie(true); c.setAcceptThirdPartyCookies(webView,true);
        webView.getSettings().setJavaScriptEnabled(true); webView.getSettings().setDomStorageEnabled(true); webView.getSettings().setDatabaseEnabled(true);
        webView.getSettings().setUserAgentString("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36");
        webView.setWebViewClient(new WebViewClient(){ @Override public boolean shouldOverrideUrlLoading(WebView v, WebResourceRequest r){String s=r.getUrl().getScheme();return !("http".equals(s)||"https".equals(s));} });
    }
    private void openLogin(){status.setText("请在页面内完成登录。");webView.loadUrl(LOGIN_URL);}
    private void checkSession(){CookieManager.getInstance().flush();String c=CookieManager.getInstance().getCookie(COOKIE_URL);status.setText(c!=null&&!c.isEmpty()?"发现会话 Cookie。":"没有发现会话，请先登录。");}
    private void askStartAgent(){
        EditText input=new EditText(this); input.setHint("Render 的 AUTH_TOKEN"); input.setSingleLine(true);
        new AlertDialog.Builder(this).setTitle("启动后台读取代理").setMessage("会显示常驻通知；它只执行收藏、点赞和附件的读取请求。")
            .setView(input).setNegativeButton("取消",null).setPositiveButton("启动",(d,w)->{
                String token=input.getText().toString().trim(); if(token.isEmpty()){status.setText("需要 AUTH_TOKEN。");return;}
                getSharedPreferences("agent",MODE_PRIVATE).edit().putString("token",token).apply();
                startForegroundService(new Intent(this,AgentService.class)); status.setText("读取代理已启动，可离开本 App。");
            }).show();
    }
    @Override protected void onPause(){CookieManager.getInstance().flush();super.onPause();}
}