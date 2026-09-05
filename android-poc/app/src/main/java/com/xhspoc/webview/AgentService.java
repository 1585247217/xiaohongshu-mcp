package com.xhspoc.webview;

import android.annotation.SuppressLint;
import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.Service;
import android.content.Context;
import android.content.Intent;
import android.os.Build;
import android.os.Handler;
import android.os.IBinder;
import android.webkit.CookieManager;
import android.webkit.DownloadListener;
import android.webkit.WebView;
import android.webkit.WebViewClient;

import org.json.JSONObject;
import org.json.JSONTokener;

import java.io.OutputStream;
import java.net.HttpURLConnection;
import java.net.URL;
import java.nio.charset.StandardCharsets;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

// A foreground service is required for Android to keep this WebView session
// alive. It accepts only read-only jobs from the user's own Render service.
public final class AgentService extends Service {
    private static final String BASE = "https://xiaohongshu-mcp-read.onrender.com/api/v1/mobile/agent";
    private static final String CHANNEL = "xhs_agent";
    private final Handler ui = new Handler();
    private final ExecutorService network = Executors.newSingleThreadExecutor();
    private WebView web;
    private String token;
    private boolean stopped;

    @Override public void onCreate() {
        super.onCreate();
        token = getSharedPreferences("agent", MODE_PRIVATE).getString("token", "");
        createChannel();
        startForeground(7, notification("小红书读取代理在线"));
        configureWebView();
        poll();
    }

    @SuppressLint("SetJavaScriptEnabled")
    private void configureWebView() {
        web = new WebView(getApplicationContext());
        CookieManager.getInstance().setAcceptCookie(true);
        web.getSettings().setJavaScriptEnabled(true);
        web.getSettings().setDomStorageEnabled(true);
        web.setWebViewClient(new WebViewClient());
        web.setDownloadListener(new DownloadListener() {
            @Override public void onDownloadStart(String url, String agent, String disposition, String mime, long size) {
                if (activeJob != null) finish(activeJob, new JSONObject(), "", url);
            }
        });
    }

    private String activeJob;

    private void poll() {
        if (stopped) return;
        network.execute(() -> {
            try {
                HttpURLConnection c = open("GET", BASE + "/jobs/next");
                int code = c.getResponseCode();
                if (code == 200) {
                    java.io.InputStream in = c.getInputStream();
                    byte[] body = readAll(in);
                    JSONObject job = new JSONObject(new String(body, StandardCharsets.UTF_8));
                    ui.post(() -> runJob(job));
                }
                c.disconnect();
            } catch (Exception ignored) { }
            ui.postDelayed(this::poll, 5000);
        });
    }

    private void runJob(JSONObject job) {
        activeJob = job.optString("id");
        JSONObject payload = job.optJSONObject("payload");
        if (payload == null) { finish(activeJob, null, "任务格式错误", null); return; }
        if ("profile".equals(job.optString("kind"))) {
            readProfile(activeJob, payload.optString("tab"));
        } else if ("attachment".equals(job.optString("kind"))) {
            readAttachment(activeJob, payload.optString("url"));
        } else {
            finish(activeJob, null, "不支持的只读任务", null);
        }
    }

    private void readProfile(String id, String tab) {
        web.loadUrl("https://www.xiaohongshu.com/explore");
        ui.postDelayed(() -> web.evaluateJavascript("(() => { const a=[...document.querySelectorAll('a[href*="/user/profile/"]')][0]; if(a){a.click();return true;} const n=[...document.querySelectorAll('*')].find(e=>['我','我的'].includes((e.innerText||'').trim())); if(n){n.click();return true;} return false; })()", ignored ->
            ui.postDelayed(() -> web.evaluateJavascript("(() => { const n=[...document.querySelectorAll('*')].find(e=>(e.innerText||'').trim()==='" + ("liked".equals(tab) ? "点赞" : "收藏") + "'); if(n) n.click(); return true; })()", ignored2 ->
                ui.postDelayed(() -> web.evaluateJavascript("(() => JSON.stringify({url:location.href,text:(document.body?.innerText||'').slice(0,12000)}))()", value -> {
                    try { finish(id, (JSONObject) new JSONTokener(value).nextValue(), "", null); }
                    catch (Exception e) { finish(id, null, "无法读取私密页面", null); }
                }), 3000)), 3000)), 3000);
    }

    private void readAttachment(String id, String url) {
        if (url == null || url.isEmpty()) { finish(id, null, "缺少附件地址", null); return; }
        web.loadUrl(url);
        ui.postDelayed(() -> web.evaluateJavascript("(() => { const n=[...document.querySelectorAll('button,a,div')].find(e=>/下载/.test((e.innerText||'').trim()) && e.getBoundingClientRect().width>0); if(n){n.click();return true;} return false; })()", clicked -> {
            if (!"true".equals(clicked)) finish(id, null, "附件页未找到下载入口", null);
        }), 5000);
    }

    private void finish(String id, JSONObject result, String error, String downloadUrl) {
        if (id == null || id.isEmpty()) return;
        if (result == null) result = new JSONObject();
        if (downloadUrl != null) try { result.put("download_url", downloadUrl); } catch (Exception ignored) {}
        final JSONObject out = result; final String failure = error == null ? "" : error;
        network.execute(() -> {
            try {
                HttpURLConnection c = open("POST", BASE + "/jobs/" + id + "/result");
                c.setDoOutput(true); c.setRequestProperty("Content-Type", "application/json");
                JSONObject body = new JSONObject(); body.put("result", out); body.put("error", failure);
                try (OutputStream stream = c.getOutputStream()) { stream.write(body.toString().getBytes(StandardCharsets.UTF_8)); }
                c.getResponseCode(); c.disconnect();
            } catch (Exception ignored) { }
        });
        activeJob = null;
    }

    private HttpURLConnection open(String method, String raw) throws Exception {
        HttpURLConnection c=(HttpURLConnection)new URL(raw).openConnection();
        c.setRequestMethod(method); c.setConnectTimeout(15000); c.setReadTimeout(30000);
        c.setRequestProperty("Authorization", "Bearer " + token);
        return c;
    }
    private static byte[] readAll(java.io.InputStream in) throws Exception {
        java.io.ByteArrayOutputStream out=new java.io.ByteArrayOutputStream(); byte[] b=new byte[4096]; int n;
        while((n=in.read(b))>=0) out.write(b,0,n); return out.toByteArray();
    }

    private void createChannel() {
        if (Build.VERSION.SDK_INT >= 26) {
            NotificationChannel c=new NotificationChannel(CHANNEL,"小红书读取代理",NotificationManager.IMPORTANCE_LOW);
            getSystemService(NotificationManager.class).createNotificationChannel(c);
        }
    }
    private Notification notification(String text) {
        Notification.Builder b=Build.VERSION.SDK_INT>=26?new Notification.Builder(this,CHANNEL):new Notification.Builder(this);
        return b.setContentTitle("小红书读取代理").setContentText(text).setSmallIcon(android.R.drawable.stat_notify_sync).build();
    }
    @Override public int onStartCommand(Intent i,int flags,int startId){ return START_STICKY; }
    @Override public void onDestroy(){ stopped=true; if(web!=null) web.destroy(); network.shutdownNow(); super.onDestroy(); }
    @Override public IBinder onBind(Intent i){ return null; }
}