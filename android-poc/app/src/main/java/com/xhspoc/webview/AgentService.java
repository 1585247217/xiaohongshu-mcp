package com.xhspoc.webview;

import android.annotation.SuppressLint;
import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.Service;
import android.content.Intent;
import android.os.Build;
import android.os.Handler;
import android.os.IBinder;
import android.webkit.CookieManager;
import android.webkit.DownloadListener;
import android.webkit.JavascriptInterface;
import android.webkit.WebView;
import android.webkit.WebViewClient;

import org.json.JSONObject;
import org.json.JSONTokener;

import java.net.URLEncoder;
import java.nio.charset.StandardCharsets;

// The token remains in this app's private storage. It is sent exactly once in
// the WebSocket handshake; no Chat request ever contains it.
public final class AgentService extends Service {
    private static final String SOCKET = "wss://xiaohongshu-mcp-read.onrender.com/api/v1/mobile/agent/ws";
    private static final String CHANNEL = "xhs_agent";
    private final Handler ui = new Handler();
    private WebView bridge, reader;
    private String token, activeJob;
    private boolean stopped;

    @Override public void onCreate() {
        super.onCreate();
        token=getSharedPreferences("agent",MODE_PRIVATE).getString("token","");
        createChannel(); startForeground(7,notification("正在连接 Render"));
        configureReader(); configureBridge();
    }

    @SuppressLint("SetJavaScriptEnabled") private void configureReader() {
        reader=new WebView(getApplicationContext());
        CookieManager.getInstance().setAcceptCookie(true);
        reader.getSettings().setJavaScriptEnabled(true); reader.getSettings().setDomStorageEnabled(true); reader.getSettings().setDatabaseEnabled(true);
        reader.getSettings().setUserAgentString("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36");
        CookieManager.getInstance().setAcceptThirdPartyCookies(reader,true);
        reader.setWebViewClient(new WebViewClient());
        reader.setDownloadListener(new DownloadListener() {
            @Override public void onDownloadStart(String url,String agent,String disposition,String mime,long size) {
                if(activeJob!=null) sendResult(activeJob,new JSONObject(),"",url);
            }
        });
    }

    @SuppressLint({"SetJavaScriptEnabled","AddJavascriptInterface"}) private void configureBridge() {
        bridge=new WebView(getApplicationContext());
        bridge.getSettings().setJavaScriptEnabled(true);
        bridge.addJavascriptInterface(new Object() {
            @JavascriptInterface public void onMessage(String raw) { ui.post(()->handleMessage(raw)); }
            @JavascriptInterface public void onSocketState(String state) { ui.post(()->setConnectionState(state)); }
            @JavascriptInterface public void onSocketClosed() {
                ui.post(()->setConnectionState("已断开，5 秒后重连"));
                ui.postDelayed(AgentService.this::connectBridge,5000);
            }
        },"NativeAgent");
        connectBridge();
    }

    private void connectBridge() {
        if(stopped || token.isEmpty()) return;
        try {
            String url=SOCKET;
            String html="<script>window.agentSocket=new WebSocket("+JSONObject.quote(url)+");"+
                "agentSocket.onopen=()=>agentSocket.send(JSON.stringify({type:'auth',token:"+JSONObject.quote(token)+"}));"+
                "agentSocket.onmessage=e=>{try{const m=JSON.parse(e.data);if(m.type==='auth_ok')NativeAgent.onSocketState('已连接');else{agentSocket.send(JSON.stringify({type:'event',stage:'JS 已收到服务器任务'}));NativeAgent.onMessage(e.data)}}catch(x){NativeAgent.onSocketState('消息解析失败')}};"+
                "agentSocket.onerror=()=>NativeAgent.onSocketState('连接失败');"+
                "agentSocket.onclose=()=>NativeAgent.onSocketClosed();</script>";
            bridge.loadDataWithBaseURL("https://xiaohongshu-mcp-read.onrender.com",html,"text/html","UTF-8",null);
        } catch(Exception ignored) { ui.postDelayed(this::connectBridge,5000); }
    }

    private void handleMessage(String raw) {
        try {
            JSONObject command=new JSONObject(raw);
            if(!"command".equals(command.optString("type"))) return;
            activeJob=command.optString("id");
            sendEvent("收到任务："+command.optString("kind"));
            JSONObject payload=command.optJSONObject("payload");
            if(payload==null) { sendResult(activeJob,null,"任务格式错误",null); return; }
            if("favorites".equals(command.optString("kind"))) readFavorites(activeJob);
            else if("attachment".equals(command.optString("kind"))) readAttachment(activeJob,payload.optString("url"));
            else sendResult(activeJob,null,"不支持的只读任务",null);
        } catch(Exception ignored) { }
    }

    private void readFavorites(String id) {
        final String requested="https://www.xiaohongshu.com/explore";
        sendEvent("LOAD_TARGET");
        ui.postDelayed(() -> { if(id.equals(activeJob)) sendResult(id,null,"FAVORITES_TIMEOUT",null); },22000);
        reader.loadUrl(requested);
        ui.postDelayed(() -> {
            sendEvent("CHECK_SESSION");
            String findProfile="(() => {const links=[...document.querySelectorAll('a[href*=\\'/user/profile/\\']')];"+
                "const a=links.find(x=>!x.closest('section.note-item')&&(x.closest('nav')||x.closest('header')||x.closest('[class*=side]')||/我|主页/.test(x.innerText||'')))||links[links.length-1];"+
                "return JSON.stringify({requested_url:"+JSONObject.quote(requested)+",final_url:location.href,title:document.title,cookie_present:document.cookie.length>0,cookie_names:[...new Set(document.cookie.split(';').map(x=>x.trim().split('=')[0]).filter(Boolean))].slice(0,12),profile_url:a?a.href:'',preview:(document.body?.innerText||'').slice(0,300)});})()";
            reader.evaluateJavascript(findProfile,value -> {
                try {
                    Object decoded=new JSONTokener(value).nextValue();
                    if(decoded instanceof String) decoded=new JSONTokener((String)decoded).nextValue();
                    JSONObject meta=(JSONObject)decoded;
                    String profile=meta.optString("profile_url");
                    if(profile.isEmpty()){ meta.put("state",meta.optBoolean("cookie_present")?"WRONG_PAGE":"LOGIN_REQUIRED"); sendResult(id,meta,"",null); return; }
                    sendEvent("LOAD_PROFILE");
                    reader.loadUrl(profile);
                    ui.postDelayed(() -> reader.evaluateJavascript("(() => {const n=[...document.querySelectorAll('button,a,div')].find(e=>(e.innerText||'').trim()==='收藏');if(n){n.click();return true;}return false;})()",clicked -> {
                        sendEvent("LOAD_FAVORITES");
                        ui.postDelayed(() -> {
                            String extract="(() => {const cards=[...document.querySelectorAll('section.note-item,[class*=note-item]')].slice(0,3).map(n=>{const a=n.querySelector('a[href]');const t=n.querySelector('[class*=title]');return {title:(t?.innerText||n.innerText||'').trim().split('\\n')[0],url:a?a.href:''}});"+
                                "return JSON.stringify({state:cards.length?'FAVORITES_OK':'PROFILE_OK_NO_ITEMS',requested_url:"+JSONObject.quote(requested)+",final_url:location.href,title:document.title,cookie_present:document.cookie.length>0,cookie_names:[...new Set(document.cookie.split(';').map(x=>x.trim().split('=')[0]).filter(Boolean))].slice(0,12),preview:(document.body?.innerText||'').slice(0,300),favorites:cards});})()";
                            reader.evaluateJavascript(extract,out -> {
                                try { Object d=new JSONTokener(out).nextValue(); if(d instanceof String)d=new JSONTokener((String)d).nextValue(); sendResult(id,(JSONObject)d,"",null); }
                                catch(Exception e){ sendResult(id,null,"FAVORITES_PARSE_ERROR",null); }
                            });
                        },4000);
                    }),5000);
                } catch(Exception e){ sendResult(id,null,"SESSION_DIAGNOSTIC_ERROR",null); }
            });
        },6000);
    }

    private void readAttachment(String id,String url) {
        if(url==null||url.isEmpty()) { sendResult(id,null,"缺少附件地址",null); return; }
        reader.loadUrl(url);
        ui.postDelayed(() -> reader.evaluateJavascript("(() => {const n=[...document.querySelectorAll('button,a,div')].find(e=>/下载/.test((e.innerText||'').trim())&&e.getBoundingClientRect().width>0);if(n){n.click();return true;}return false;})()", clicked -> {
            if(!"true".equals(clicked)) sendResult(id,null,"附件页未找到下载入口",null);
        }),5000);
    }

    private void sendEvent(String stage) {
        setConnectionState(stage);
        try {
            JSONObject packet=new JSONObject(); packet.put("type","event"); packet.put("stage",stage);
            bridge.evaluateJavascript("if(window.agentSocket&&agentSocket.readyState===1)agentSocket.send("+JSONObject.quote(packet.toString())+")",null);
        } catch(Exception ignored) { }
    }

    private void sendResult(String id,JSONObject result,String error,String downloadUrl) {
        if(id==null||id.isEmpty()) return;
        if(result==null) result=new JSONObject();
        try { if(downloadUrl!=null) result.put("download_url",downloadUrl); } catch(Exception ignored) {}
        try {
            JSONObject packet=new JSONObject(); packet.put("type","result"); packet.put("id",id); packet.put("result",result); packet.put("error",error==null?"":error);
            bridge.evaluateJavascript("if(window.agentSocket&&agentSocket.readyState===1)agentSocket.send("+JSONObject.quote(packet.toString())+")",null);
        } catch(Exception ignored) { }
        setConnectionState(error==null||error.isEmpty()?"任务已回传":error);
        activeJob=null;
    }

    private void setConnectionState(String state) {
        getSharedPreferences("agent",MODE_PRIVATE).edit().putString("connection_state",state).apply();
        getSystemService(NotificationManager.class).notify(7,notification(state));
    }

    private void createChannel() {
        if(Build.VERSION.SDK_INT>=26) getSystemService(NotificationManager.class).createNotificationChannel(new NotificationChannel(CHANNEL,"小红书读取代理",NotificationManager.IMPORTANCE_LOW));
    }
    private Notification notification(String text) {
        Notification.Builder b=Build.VERSION.SDK_INT>=26?new Notification.Builder(this,CHANNEL):new Notification.Builder(this);
        return b.setContentTitle("小红书读取代理").setContentText(text).setSmallIcon(android.R.drawable.stat_notify_sync).build();
    }
    @Override public int onStartCommand(Intent i,int flags,int startId){return START_STICKY;}
    @Override public void onDestroy(){stopped=true;if(bridge!=null)bridge.destroy();if(reader!=null)reader.destroy();super.onDestroy();}
    @Override public IBinder onBind(Intent i){return null;}
}