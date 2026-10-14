package setup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"appguard/internal/config"
)

type setupRequest struct {
	Role          string               `json:"role"`
	Group         string               `json:"group"`
	ControllerURL string               `json:"controllerUrl"`
	Token         string               `json:"token"`
	CheckEvery    int                  `json:"checkEverySeconds"`
	Applications  []config.Application `json:"applications"`
}

func Run(ctx context.Context, configPath string) error {
	host, _ := os.Hostname()
	token := randomToken()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()

	done := make(chan error, 1)
	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		page := strings.ReplaceAll(setupHTML, "{{PC_NAME}}", htmlEscape(host))
		page = strings.ReplaceAll(page, "{{TOKEN}}", htmlEscape(token))
		_, _ = w.Write([]byte(page))
	})

	mux.HandleFunc("/api/select-app", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		path, err := pickExecutable()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{
			"path": path,
			"name": strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)),
			"workingDirectory": filepath.Dir(path),
		})
	})

	mux.HandleFunc("/api/save", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req setupRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Role != "controller" && req.Role != "agent" {
			http.Error(w, "role must be controller or agent", http.StatusBadRequest)
			return
		}
		if req.Token == "" {
			http.Error(w, "token is required", http.StatusBadRequest)
			return
		}
		if req.Role == "agent" && strings.TrimSpace(req.ControllerURL) == "" {
			http.Error(w, "controller URL is required for agents", http.StatusBadRequest)
			return
		}
		if len(req.Applications) == 0 {
			http.Error(w, "select at least one application to monitor", http.StatusBadRequest)
			return
		}

		cfg := &config.Config{
			AgentID: host,
			Role: req.Role,
			Group: req.Group,
			Enabled: true,
			CheckEvery: req.CheckEvery,
			Controller: config.Controller{
				URL: strings.TrimSpace(req.ControllerURL),
				Token: req.Token,
				PollEverySeconds: 5,
				Listen: ":8443",
				DatabasePath: "appguard.db",
			},
			Apps: req.Applications,
		}
		config.Normalize(cfg)

		if req.Role == "controller" {
			cfg.Controller.URL = ""
			if cfg.Group == "default" {
				cfg.Group = "Controllers"
			}
		}

		if err := config.Save(configPath, cfg); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		writeJSON(w, map[string]any{
			"saved": true,
			"configPath": configPath,
			"agentId": cfg.AgentID,
			"role": cfg.Role,
		})

		select {
		case done <- nil:
		default:
		}
	})

	server := &http.Server{
		Handler: mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		_ = server.Serve(listener)
	}()

	url := "http://" + listener.Addr().String() + "/"
	if err := openBrowser(url); err != nil {
		return fmt.Errorf("open setup browser: %w", err)
	}

	select {
	case <-ctx.Done():
		_ = server.Shutdown(context.Background())
		return ctx.Err()
	case err := <-done:
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		return err
	}
}

func randomToken() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "CHANGE-THIS-TOKEN"
	}
	return hex.EncodeToString(b[:])
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func htmlEscape(value string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		"\"", "&quot;",
		"'", "&#39;",
	)
	return replacer.Replace(value)
}

const setupHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>AppGuard Setup</title>
<style>
:root{font-family:Inter,Segoe UI,system-ui,sans-serif;color:#e8edf7;background:#07111f}
*{box-sizing:border-box}body{margin:0;min-height:100vh;background:linear-gradient(140deg,#06101d,#0d2138)}
main{max-width:940px;margin:auto;padding:42px 22px}.hero{margin-bottom:24px}.hero h1{margin:0 0 8px;font-size:30px}.muted{color:#91a6be}
.panel{background:#0d1c2d;border:1px solid #20344e;border-radius:16px;padding:20px;margin:16px 0;box-shadow:0 12px 34px #0004}
.grid{display:grid;grid-template-columns:1fr 1fr;gap:14px}.full{grid-column:1/-1}
label{display:block;font-size:12px;color:#91a6be;margin-bottom:6px}
input,select{width:100%;padding:11px 12px;background:#07111f;color:#fff;border:1px solid #2b4564;border-radius:9px}
.role{display:grid;grid-template-columns:1fr 1fr;gap:12px}.role button{padding:16px;border-radius:12px;border:1px solid #2b4564;background:#10243a;color:#fff;cursor:pointer}
.role button.active{background:#195239;border-color:#3b996c}.btn{padding:10px 14px;border:1px solid #2b4564;border-radius:9px;background:#183451;color:#fff;cursor:pointer}.btn.good{background:#195239}.btn.warn{background:#5b2c22}.row{display:flex;gap:8px;align-items:end}.grow{flex:1}
.app{border:1px solid #20344e;border-radius:12px;padding:14px;margin:12px 0}.app-head{display:flex;justify-content:space-between;align-items:center;margin-bottom:12px}
.actions{display:flex;justify-content:space-between;gap:10px;margin-top:18px}.success{padding:14px;background:#123b2a;border:1px solid #2d7655;border-radius:10px;display:none}
@media(max-width:720px){.grid,.role{grid-template-columns:1fr}.full{grid-column:auto}.row{flex-direction:column;align-items:stretch}}
</style>
</head>
<body>
<main>
<div class="hero"><h1>AppGuard First-Run Setup</h1><div class="muted">Configure this computer without editing JSON manually.</div></div>

<div class="panel">
<h2>1. This computer</h2>
<div class="grid">
<div><label>Computer / Agent ID</label><input id="agentId" value="{{PC_NAME}}" readonly></div>
<div><label>Group</label><input id="group" value="default" placeholder="e.g. NUQs, Controllers"></div>
</div>
</div>

<div class="panel">
<h2>2. Choose this node's role</h2>
<div class="role">
<button id="roleController" onclick="setRole('controller')"><b>Controller</b><br><span class="muted">Dashboard + central control + local watchdog</span></button>
<button id="roleAgent" onclick="setRole('agent')"><b>Agent</b><br><span class="muted">Local watchdog connected to a controller</span></button>
</div>
<div id="agentControllerFields" class="grid" style="margin-top:16px;display:none">
<div class="full"><label>Controller URL</label><input id="controllerUrl" placeholder="http://192.168.1.10:8443"></div>
</div>
<div class="grid" style="margin-top:16px">
<div class="full"><label>Shared token</label><input id="token" value="{{TOKEN}}"></div>
<div><label>Watchdog check interval (seconds)</label><input id="checkEvery" type="number" min="1" value="10"></div>
</div>
</div>

<div class="panel">
<div class="app-head"><div><h2 style="margin:0">3. Applications to monitor</h2><div class="muted">Select one or more Windows executables.</div></div><button class="btn" onclick="addApp()">+ Add application</button></div>
<div id="apps"></div>
</div>

<div class="panel">
<div id="success" class="success"></div>
<div class="actions"><span class="muted">The generated file will be saved as appguard.json.</span><button class="btn good" onclick="save()">Save configuration & start AppGuard</button></div>
</div>
</main>
<script>
let role='';
let apps=[];

function setRole(next){
 role=next;
 document.getElementById('roleController').classList.toggle('active',next==='controller');
 document.getElementById('roleAgent').classList.toggle('active',next==='agent');
 document.getElementById('agentControllerFields').style.display=next==='agent'?'grid':'none';
 const group=document.getElementById('group');
 if(next==='controller' && (group.value==='default'||group.value==='')) group.value='Controllers';
 if(next==='agent' && group.value==='Controllers') group.value='default';
}

function addApp(app){
 apps.push(app||{
  name:'',
  path:'',
  workingDirectory:'',
  enabled:true,
  autoRestart:true,
  maxRetries:5,
  resetAfterSeconds:300,
  startupDelaySeconds:60
 });
 renderApps();
}

function removeApp(i){apps.splice(i,1);renderApps()}

async function selectApp(i){
 const r=await fetch('/api/select-app',{method:'POST'});
 if(!r.ok){const t=await r.text();if(!t.toLowerCase().includes('cancel')) alert(t);return}
 const d=await r.json();
 apps[i].path=d.path;
 apps[i].name=apps[i].name||d.name;
 apps[i].workingDirectory=d.workingDirectory;
 renderApps();
}

function update(i,key,value){
 if(['maxRetries','resetAfterSeconds','startupDelaySeconds'].includes(key)) value=Number(value);
 apps[i][key]=value;
}

function esc(v){return String(v??'').replace(/[&<>"]/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c]))}

function renderApps(){
 document.getElementById('apps').innerHTML=apps.map((a,i)=>`
 <div class="app">
  <div class="app-head"><b>Application ${i+1}</b><button class="btn warn" onclick="removeApp(${i})">Remove</button></div>
  <div class="grid">
   <div><label>Name</label><input value="${esc(a.name)}" oninput="update(${i},'name',this.value)"></div>
   <div class="row"><div class="grow"><label>Executable</label><input value="${esc(a.path)}" readonly></div><button class="btn" onclick="selectApp(${i})">Browse…</button></div>
   <div class="full"><label>Working directory</label><input value="${esc(a.workingDirectory)}" oninput="update(${i},'workingDirectory',this.value)"></div>
   <div><label>Initial startup delay (seconds)</label><input type="number" min="0" value="${a.startupDelaySeconds}" oninput="update(${i},'startupDelaySeconds',this.value)"></div>
   <div><label>Max retries</label><input type="number" min="1" value="${a.maxRetries}" oninput="update(${i},'maxRetries',this.value)"></div>
   <div><label>Retry reset after stable seconds</label><input type="number" min="1" value="${a.resetAfterSeconds}" oninput="update(${i},'resetAfterSeconds',this.value)"></div>
  </div>
 </div>`).join('');
}

async function save(){
 if(!role){alert('Choose Controller or Agent.');return}
 if(!apps.length||apps.some(a=>!a.path)){alert('Select at least one application executable.');return}
 const body={
  role,
  group:document.getElementById('group').value,
  controllerUrl:document.getElementById('controllerUrl').value,
  token:document.getElementById('token').value,
  checkEverySeconds:Number(document.getElementById('checkEvery').value),
  applications:apps
 };
 const r=await fetch('/api/save',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});
 if(!r.ok){alert(await r.text());return}
 const d=await r.json();
 const s=document.getElementById('success');
 s.style.display='block';
 s.textContent='Configuration saved for '+d.agentId+'. You can close this browser window; AppGuard is starting.';
}

addApp();
</script>
</body>
</html>`
