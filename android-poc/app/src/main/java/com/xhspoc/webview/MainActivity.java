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
import android.widget.ScrollView;

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
    private void checkSession(){
        CookieManager.getInstance().flush();
        String c=CookieManager.getInstance().getCookie(COOKIE_URL);
        String message=c!=null&&!c.isEmpty()?"已发现登录会话 Cookie。":"没有发现会话，请先登录。";
        status.setText("会话已检查");
        showResult("会话检查",message);
    }
    private void showResult(String title,String message){
        TextView text=new TextView(this); text.setText(message); text.setTextSize(16); text.setPadding(40,28,40,28); text.setTextIsSelectable(true);
        ScrollView scroll=new ScrollView(this); scroll.addView(text);
        new AlertDialog.Builder(this).setTitle(title).setView(scroll).setPositiveButton("知道了",null).show();
    }
    private void askStartAgent(){
        String saved=getSharedPreferences("agent",MODE_PRIVATE).getString("token","");
        if(!saved.isEmpty()){ startAgent(); return; }
        EditText input=new EditText(this); input.setHint("Render 的 AUTH_TOKEN"); input.setSingleLine(true);
        new AlertDialog.Builder(this).setTitle("首次启动读取代理").setMessage("AUTH_TOKEN 只保存在手机本地，以后启动不再询问。")
            .setView(input).setNegativeButton("取消",null).setPositiveButton("启动",(d,w)->{
                String token=input.getText().toString().trim(); if(token.isEmpty()){showResult("启动失败","需要 AUTH_TOKEN。");return;}
                getSharedPreferences("agent",MODE_PRIVATE).edit().putString("token",token).apply(); startAgent();
            }).show();
    }
    private void startAgent(){
        getSharedPreferences("agent",MODE_PRIVATE).edit().putString("connection_state","正在连接").apply();
        startForegroundService(new Intent(this,AgentService.class)); status.setText("读取代理正在连接，可查看通知栏状态。");
        new android.os.Handler().postDelayed(()->showResult("读取代理",getSharedPreferences("agent",MODE_PRIVATE).getString("connection_state","正在连接")),3500);
    }
    @Override protected void onPause(){CookieManager.getInstance().flush();super.onPause();}
}