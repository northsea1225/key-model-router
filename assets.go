package main

const indexHTML = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="referrer" content="no-referrer">
<title>Key Model Router</title>
<link rel="stylesheet" href="./app.css">
</head>
<body>
<div id="root" class="wrap"><p class="muted">加载中…</p></div>
<script src="./app.js"></script>
</body>
</html>
`

const appCSS = `:root{color-scheme:light dark;--bg:#f6f7f9;--panel:#fff;--text:#1d2430;--muted:#637083;--line:#d9dee6;--accent:#1f6feb;--danger:#b42318;--danger-bg:#fff1f0;--ok:#157347}
@media (prefers-color-scheme:dark){:root{--bg:#101318;--panel:#171b22;--text:#e7ebf0;--muted:#98a3b3;--line:#2a303a;--accent:#6ea8fe;--danger:#ff8a80;--danger-bg:#2a1414;--ok:#6ee7a8}}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--text);font:14px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC","Hiragino Sans GB","Microsoft YaHei",sans-serif}
.wrap{max-width:1080px;margin:0 auto;padding:28px 20px 60px}
h1{font-size:20px;margin:0}
.top{display:flex;align-items:center;justify-content:space-between;gap:16px;margin-bottom:6px}
.sub{color:var(--muted);font-size:13px;margin:0 0 18px}
.panel{background:var(--panel);border:1px solid var(--line);border-radius:10px;overflow:hidden}
table{width:100%;border-collapse:collapse}
th,td{padding:10px 12px;border-bottom:1px solid var(--line);text-align:left;vertical-align:middle}
th{font-size:12px;color:var(--muted);font-weight:600;background:color-mix(in srgb,var(--panel) 88%,var(--bg))}
tr:last-child td{border-bottom:0}
tr.bad td{background:var(--danger-bg)}
.masked{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:12px;color:var(--muted);white-space:nowrap}
code{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:12px}
input[type=text],input[type=password],select{width:100%;min-width:0;padding:7px 9px;border:1px solid var(--line);border-radius:7px;background:var(--panel);color:var(--text);font:inherit}
input[type=text]:focus,input[type=password]:focus,select:focus{outline:2px solid color-mix(in srgb,var(--accent) 45%,transparent);outline-offset:1px}
button{padding:7px 14px;border:1px solid var(--line);border-radius:7px;background:var(--panel);color:var(--text);font:inherit;cursor:pointer}
button.primary{background:var(--accent);border-color:var(--accent);color:#fff}
button:disabled{opacity:.55;cursor:not-allowed}
.toolbar{display:flex;align-items:center;gap:12px;margin:0 0 12px;flex-wrap:wrap}
.muted{color:var(--muted)}
.ok{color:var(--ok)}
.err{color:var(--danger)}
.login{max-width:360px;margin:12vh auto 0;padding:22px;background:var(--panel);border:1px solid var(--line);border-radius:12px}
.login h1{margin-bottom:4px}
.login .row{margin:14px 0}
.login button{width:100%}
.hint{font-size:12px;color:var(--muted);margin-top:10px}
.warn{background:var(--danger-bg);border:1px solid color-mix(in srgb,var(--danger) 35%,var(--line));color:var(--text);padding:8px 10px;border-radius:7px;font-size:12px;margin:0 0 12px}
.target-cell{min-width:220px}
.nowrap{white-space:nowrap}
@media (max-width:720px){th:nth-child(3),td:nth-child(3){display:none}.wrap{padding:18px 12px 40px}}
`

const appJS = `(function(){
"use strict";
var BASE="/v0/resource/plugins/key-model-router";
var CSRF="";
var busy=false;
var root=document.getElementById("root");

function el(tag,cls,text){var n=document.createElement(tag);if(cls)n.className=cls;if(text!==undefined&&text!==null)n.textContent=String(text);return n;}
function clear(node){while(node.firstChild)node.removeChild(node.firstChild);}
function fmt(ts){if(!ts)return "—";var d=new Date(ts);if(isNaN(d.getTime()))return ts;return d.toLocaleString();}

function api(path,opts){
  opts=opts||{};
  var headers={"Accept":"application/json"};
  if(opts.body!==undefined)headers["Content-Type"]="application/json";
  if(CSRF)headers["X-KMR-CSRF"]=CSRF;
  if(opts.headers){for(var k in opts.headers)headers[k]=opts.headers[k];}
  return fetch(BASE+path,{method:opts.method||"GET",credentials:"same-origin",headers:headers,body:opts.body}).then(function(res){
    return res.text().then(function(text){
      var data=null;if(text){try{data=JSON.parse(text);}catch(e){data=null;}}
      if(res.status===401){showLogin();var e401=new Error("unauthorized");e401.status=401;throw e401;}
      if(!res.ok){var err=new Error((data&&data.error)||("HTTP "+res.status));err.status=res.status;err.data=data;throw err;}
      return data||{};
    });
  });
}

function showLogin(message){
  clear(root);
  var box=el("div","login");
  box.appendChild(el("h1",null,"Key Model Router"));
  box.appendChild(el("p","sub","输入管理密码以编辑按 key 的模型路由。"));
  var row=el("div","row");
  var input=document.createElement("input");
  input.type="password";input.autocomplete="current-password";input.placeholder="管理密码";
  row.appendChild(input);box.appendChild(row);
  var btn=el("button","primary","登录");box.appendChild(btn);
  var msg=el("p","err",message||"");box.appendChild(msg);
  box.appendChild(el("p","hint","密码只用于本插件，不会写入 CPA 配置或浏览器存储。"));
  root.appendChild(box);
  input.focus();
  function submit(){
    if(busy)return;
    var password=input.value;
    if(!password){msg.textContent="请输入密码";return;}
    busy=true;btn.disabled=true;msg.textContent="";
    api("/api/login",{method:"POST",body:JSON.stringify({password:password})}).then(function(data){
      CSRF=data.csrf||"";busy=false;btn.disabled=false;load();
    }).catch(function(err){
      busy=false;btn.disabled=false;msg.textContent=err.status===429?"尝试次数过多，请稍后再试":("登录失败："+(err.message||"unknown"));
    });
  }
  btn.addEventListener("click",submit);
  input.addEventListener("keydown",function(ev){if(ev.key==="Enter")submit();});
}

function load(){
  api("/api/state").then(function(state){
    if(state&&state.csrf)CSRF=state.csrf;
    render(state);
  }).catch(function(err){
    if(err.status!==401)showLogin("无法读取状态："+(err.message||"unknown"));
  });
}

function render(state){
  if(state&&state.csrf)CSRF=state.csrf;
  clear(root);
  var top=el("div","top");
  var title=el("div");
  title.appendChild(el("h1",null,"Key Model Router"));
  title.appendChild(el("p","sub","每个下游 API key 固定到一个目标模型；留空表示不路由。Provider 默认「CPA 自动」。"+(state.route_mode==="executor"?"（当前为 executor 兼容模式）":"")));
  top.appendChild(title);
  var logout=el("button",null,"退出登录");
  logout.addEventListener("click",function(){
    api("/api/logout",{method:"POST",body:JSON.stringify({})}).catch(function(){}).then(function(){CSRF="";showLogin("");});
  });
  top.appendChild(logout);
  root.appendChild(top);

  if(state.password_configured===false){
    root.appendChild(el("div","warn","密码文件未配置或权限不正确，登录不可用。"));
  }
  if(state.models_error){
    root.appendChild(el("div","warn","模型列表刷新失败："+state.models_error+"（仍使用上次成功列表）"));
  }
  if(state.state_error){
    root.appendChild(el("div","warn","状态文件损坏，已按“不路由”处理："+state.state_error));
  }

  var toolbar=el("div","toolbar");
  var save=el("button","primary","保存");
  var status=el("span","muted","");
  var saved=el("span","muted","最近保存："+(state.saved_at?fmt(state.saved_at):"—"));
  var modelsInfo=el("span","muted","模型 "+(state.models_count||0)+" 个");
  toolbar.appendChild(save);toolbar.appendChild(status);toolbar.appendChild(saved);toolbar.appendChild(modelsInfo);
  root.appendChild(toolbar);

  var panel=el("div","panel");
  var table=document.createElement("table");
  var thead=document.createElement("thead");
  var hr=document.createElement("tr");
  ["别名","脱敏 Key","指纹","目标模型","Provider（可选）"].forEach(function(name){hr.appendChild(el("th",null,name));});
  thead.appendChild(hr);table.appendChild(thead);
  var tbody=document.createElement("tbody");
  table.appendChild(tbody);

  var models=Array.isArray(state.models)?state.models:[];
  var modelSet={};models.forEach(function(m){modelSet[m]=true;});
  var rows=[];
  (state.accounts||[]).forEach(function(acc){
    var tr=document.createElement("tr");
    var aliasTd=document.createElement("td");
    var alias=document.createElement("input");alias.type="text";alias.value=acc.alias||"";alias.placeholder="未命名";alias.maxLength=64;
    aliasTd.appendChild(alias);tr.appendChild(aliasTd);
    tr.appendChild(el("td","masked",acc.masked||""));
    var fpTd=document.createElement("td","nowrap");fpTd.appendChild(el("code",null,(acc.fingerprint||"").slice(0,10)));tr.appendChild(fpTd);
    var targetTd=el("td","target-cell");
    var select=document.createElement("select");
    var none=document.createElement("option");none.value="";none.textContent="不路由";select.appendChild(none);
    var seen={};
    models.forEach(function(model){
      if(seen[model])return;seen[model]=true;
      var opt=document.createElement("option");opt.value=model;opt.textContent=model;select.appendChild(opt);
    });
    var target=acc.target_model||"";
    if(target&&!modelSet[target]){
      var bad=document.createElement("option");bad.value=target;bad.textContent=target+"（不存在）";select.appendChild(bad);
      tr.className="bad";
    }
    select.value=target;
    targetTd.appendChild(select);tr.appendChild(targetTd);
    var providerTd=document.createElement("td");
    var provider=document.createElement("select");
    function fillProviders(wanted){
      clear(provider);
      var model=select.value;
      var autoName=(state.auto_providers||{})[model]||(acc.auto_provider&&model===target?acc.auto_provider:"")||(state.model_providers||{})[model]||"";
      var auto=document.createElement("option");auto.value="";
      auto.textContent=autoName?("CPA 自动（→ "+autoName+"）"):"CPA 自动";
      provider.appendChild(auto);
      var list=(state.model_provider_options||{})[model]||[];
      list.forEach(function(p){
        var o=document.createElement("option");o.value=p.name;
        o.textContent=p.name+(p.priority?(" · P"+p.priority):"")+(p.credentials>1?(" · "+p.credentials+" 个凭据"):"")+(p.source==="oauth"?" · OAuth 推断":"")+(p.usable===false?" · 不可用":"");
        provider.appendChild(o);
      });
      provider.value=wanted||"";
      if(wanted&&provider.value!==wanted){
        var stale=document.createElement("option");stale.value=wanted;
        stale.textContent=wanted+" · 未在该模型的配置中";
        provider.appendChild(stale);provider.value=wanted;
      }
      provider.disabled=!model;
    }
    fillProviders(acc.target_provider||"");
    select.addEventListener("change",function(){fillProviders("");});
    providerTd.appendChild(provider);tr.appendChild(providerTd);
    tbody.appendChild(tr);
    rows.push({fingerprint:acc.fingerprint,alias:alias,select:select,provider:provider,tr:tr});
  });
  panel.appendChild(table);root.appendChild(panel);
  if(!rows.length){root.appendChild(el("p","muted","没有从 CPA config.yaml 读取到 api-keys。"));}

  function submitSave(allowRetry){
    if(busy)return;
    busy=true;save.disabled=true;status.className="muted";status.textContent="保存中…";
    var payload={rows:rows.map(function(r){return {fingerprint:r.fingerprint,alias:r.alias.value,target_model:r.select.value,target_provider:r.provider.value};}),csrf:CSRF};
    api("/api/save",{method:"POST",body:JSON.stringify(payload)}).then(function(data){
      status.className="ok";status.textContent="已保存 "+fmt(data.saved_at||new Date().toISOString());
      saved.textContent="最近保存："+fmt(data.saved_at||new Date().toISOString());
      busy=false;save.disabled=false;
    }).catch(function(err){
      if(allowRetry&&err.status===403&&/csrf/i.test(err.message||"")){
        api("/api/state").then(function(state){
          if(state&&state.csrf)CSRF=state.csrf;
          busy=false;save.disabled=false;
          submitSave(false);
        }).catch(function(stateErr){
          busy=false;save.disabled=false;
          status.className="err";status.textContent="保存失败："+(stateErr.message||"session expired");
        });
        return;
      }
      status.className="err";status.textContent="保存失败："+(err.message||"unknown");
      busy=false;save.disabled=false;
      if(err.data&&err.data.fingerprint){rows.forEach(function(r){if(r.fingerprint===err.data.fingerprint)r.tr.className="bad";});}
    });
  }
  save.addEventListener("click",function(){submitSave(true);});
}

load();
})();
`
