package control

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"appguard/internal/config"
)

const offlineAfter = 20 * time.Second
const maxEvents = 300

type controllerState struct {
	mu       sync.RWMutex
	agents   map[string]Heartbeat
	commands map[string][]Command
	events   []Event
	token    string
}

type agentView struct {
	AgentID   string         `json:"agentId"`
	Group     string         `json:"group"`
	LastSeen  time.Time      `json:"lastSeen"`
	Online    bool           `json:"online"`
	Status    map[string]any `json:"status"`
}

func RunController(cfg *config.Config) error {
	st := &controllerState{
		agents: make(map[string]Heartbeat),
		commands: make(map[string][]Command),
		events: make([]Event, 0, 64),
		token: cfg.Controller.Token,
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(dashboardHTML))
	})

	mux.HandleFunc("/api/v1/heartbeat", st.auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var hb Heartbeat
		if err := json.NewDecoder(r.Body).Decode(&hb); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if hb.AgentID == "" {
			http.Error(w, "missing agentId", http.StatusBadRequest)
			return
		}
		if hb.Group == "" {
			hb.Group = "default"
		}

		hb.Timestamp = time.Now()

		st.mu.Lock()
		_, existed := st.agents[hb.AgentID]
		st.agents[hb.AgentID] = hb
		if !existed {
			st.appendEventLocked(Event{
				Time: time.Now(), AgentID: hb.AgentID, Group: hb.Group,
				Action: "agent-online", Detail: "Agente registado no controlador",
			})
		}
		st.mu.Unlock()

		w.WriteHeader(http.StatusNoContent)
	}))

	mux.HandleFunc("/api/v1/commands/", st.auth(func(w http.ResponseWriter, r *http.Request) {
		agentID := strings.TrimPrefix(r.URL.Path, "/api/v1/commands/")
		if agentID == "" {
			http.Error(w, "missing agent id", http.StatusBadRequest)
			return
		}

		st.mu.Lock()
		cmds := st.commands[agentID]
		delete(st.commands, agentID)
		st.mu.Unlock()

		_ = json.NewEncoder(w).Encode(cmds)
	}))

	mux.HandleFunc("/api/v1/admin/agents", st.auth(func(w http.ResponseWriter, r *http.Request) {
		st.mu.RLock()
		defer st.mu.RUnlock()

		now := time.Now()
		result := make([]agentView, 0, len(st.agents))
		for _, hb := range st.agents {
			result = append(result, agentView{
				AgentID: hb.AgentID,
				Group: hb.Group,
				LastSeen: hb.Timestamp,
				Online: now.Sub(hb.Timestamp) <= offlineAfter,
				Status: hb.Status,
			})
		}
		writeJSON(w, result)
	}))

	mux.HandleFunc("/api/v1/admin/events", st.auth(func(w http.ResponseWriter, r *http.Request) {
		st.mu.RLock()
		defer st.mu.RUnlock()

		events := make([]Event, len(st.events))
		copy(events, st.events)
		writeJSON(w, events)
	}))

	mux.HandleFunc("/api/v1/admin/broadcast/", st.auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		action := strings.TrimPrefix(r.URL.Path, "/api/v1/admin/broadcast/")
		if !validAction(action) {
			http.Error(w, "invalid action", http.StatusBadRequest)
			return
		}

		st.mu.Lock()
		for id := range st.agents {
			st.enqueueLocked(id, action, "")
		}
		st.appendEventLocked(Event{
			Time: time.Now(), Action: action, Detail: "Comando global enviado",
		})
		st.mu.Unlock()

		w.WriteHeader(http.StatusAccepted)
	}))

	mux.HandleFunc("/api/v1/admin/agent/", st.auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		rest := strings.TrimPrefix(r.URL.Path, "/api/v1/admin/agent/")
		parts := strings.Split(rest, "/")
		if len(parts) != 2 || parts[0] == "" || !validAction(parts[1]) {
			http.Error(w, "expected /agent/{id}/{action}", http.StatusBadRequest)
			return
		}

		agentID, action := parts[0], parts[1]

		st.mu.Lock()
		hb, exists := st.agents[agentID]
		if !exists {
			st.mu.Unlock()
			http.Error(w, "unknown agent", http.StatusNotFound)
			return
		}
		st.enqueueLocked(agentID, action, "")
		st.appendEventLocked(Event{
			Time: time.Now(), AgentID: agentID, Group: hb.Group,
			Action: action, Detail: "Comando enviado ao agente",
		})
		st.mu.Unlock()

		w.WriteHeader(http.StatusAccepted)
	}))

	mux.HandleFunc("/api/v1/admin/group/", st.auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		rest := strings.TrimPrefix(r.URL.Path, "/api/v1/admin/group/")
		parts := strings.Split(rest, "/")
		if len(parts) != 2 || parts[0] == "" || !validAction(parts[1]) {
			http.Error(w, "expected /group/{group}/{action}", http.StatusBadRequest)
			return
		}

		group, action := parts[0], parts[1]
		count := 0

		st.mu.Lock()
		for id, hb := range st.agents {
			if hb.Group == group {
				st.enqueueLocked(id, action, "")
				count++
			}
		}
		st.appendEventLocked(Event{
			Time: time.Now(), Group: group, Action: action,
			Detail: fmt.Sprintf("Comando enviado a %d agente(s)", count),
		})
		st.mu.Unlock()

		writeJSON(w, map[string]any{"accepted": count})
	}))

	slog.Info("AppGuard controller listening", "addr", cfg.Controller.Listen)
	server := &http.Server{
		Addr: cfg.Controller.Listen,
		Handler: mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	return server.ListenAndServe()
}

func (s *controllerState) enqueueLocked(agentID, action, appName string) {
	s.commands[agentID] = append(s.commands[agentID], Command{
		ID: fmt.Sprintf("%d-%s", time.Now().UnixNano(), agentID),
		Action: action,
		AppName: appName,
	})
}

func (s *controllerState) appendEventLocked(event Event) {
	s.events = append([]Event{event}, s.events...)
	if len(s.events) > maxEvents {
		s.events = s.events[:maxEvents]
	}
}

func (s *controllerState) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.token == "" || r.Header.Get("Authorization") != "Bearer "+s.token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func validAction(a string) bool {
	switch a {
	case "disable-all", "enable-all", "stop-all", "restart-all":
		return true
	default:
		return false
	}
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

var _ = template.HTMLEscapeString

const dashboardHTML = `<!doctype html>
<html lang="pt">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>AppGuard Control Center</title>
<style>
:root{font-family:Inter,ui-sans-serif,system-ui,-apple-system,Segoe UI,sans-serif;color:#e8edf7;background:#07111f}
*{box-sizing:border-box}body{margin:0;background:linear-gradient(135deg,#07111f,#0c1d34 55%,#07111f);min-height:100vh}
header{padding:26px 32px;border-bottom:1px solid #20344e;background:#091522dd;backdrop-filter:blur(12px);position:sticky;top:0}
h1{font-size:22px;margin:0}.sub{color:#8fa5bf;font-size:13px;margin-top:4px}
main{padding:28px 32px;max-width:1500px;margin:auto}.cards{display:grid;grid-template-columns:repeat(4,minmax(150px,1fr));gap:14px;margin-bottom:24px}
.card,.panel{background:#0d1c2d;border:1px solid #20344e;border-radius:14px;box-shadow:0 8px 28px #0004}
.card{padding:18px}.metric{font-size:31px;font-weight:700}.label{font-size:12px;color:#8fa5bf;text-transform:uppercase;letter-spacing:.08em}
.toolbar{display:flex;gap:9px;flex-wrap:wrap;margin:18px 0}.btn{background:#183451;color:#fff;border:1px solid #2a4b6b;border-radius:9px;padding:9px 13px;cursor:pointer}.btn:hover{background:#214665}
.btn.warn{background:#5b2c22;border-color:#824034}.btn.good{background:#164733;border-color:#246849}
.panel{overflow:hidden;margin-bottom:22px}.panel h2{font-size:15px;margin:0;padding:15px 18px;border-bottom:1px solid #20344e}
table{width:100%;border-collapse:collapse}th,td{text-align:left;padding:12px 16px;border-bottom:1px solid #172b42;font-size:13px}th{color:#8fa5bf;font-weight:600}
.badge{display:inline-flex;align-items:center;gap:7px;padding:5px 8px;border-radius:999px;background:#17304a}.dot{width:8px;height:8px;border-radius:50%;background:#ef5d5d}.online .dot{background:#46d887}
code{color:#b9cff0}.events{max-height:300px;overflow:auto}.event{padding:10px 16px;border-bottom:1px solid #172b42;font-size:12px;color:#b9c8dc}.muted{color:#70869f}
input{background:#07111f;border:1px solid #2b4564;color:#fff;border-radius:8px;padding:9px 11px;min-width:280px}
@media(max-width:800px){.cards{grid-template-columns:1fr 1fr}main,header{padding-left:16px;padding-right:16px}table{display:block;overflow:auto}}
</style>
</head>
<body>
<header><h1>AppGuard Control Center</h1><div class="sub">Supervisão central de processos Windows na LAN</div></header>
<main>
<div class="toolbar"><input id="token" type="password" placeholder="Token Bearer do controlador"><button class="btn" onclick="saveToken()">Guardar token</button></div>
<div class="cards">
<div class="card"><div class="label">Computadores</div><div class="metric" id="total">0</div></div>
<div class="card"><div class="label">Online</div><div class="metric" id="online">0</div></div>
<div class="card"><div class="label">Offline</div><div class="metric" id="offline">0</div></div>
<div class="card"><div class="label">Processos activos</div><div class="metric" id="running">0</div></div>
</div>
<div class="toolbar">
<button class="btn good" onclick="broadcast('enable-all')">Activar todos</button>
<button class="btn" onclick="broadcast('restart-all')">Reiniciar todos</button>
<button class="btn warn" onclick="broadcast('stop-all')">Parar todos</button>
<button class="btn warn" onclick="broadcast('disable-all')">Desactivar watchdog</button>
</div>
<section class="panel">
<h2>Agentes</h2>
<table><thead><tr><th>Computador</th><th>Grupo</th><th>Estado</th><th>Processos</th><th>Último contacto</th><th>Acções</th></tr></thead><tbody id="agents"></tbody></table>
</section>
<section class="panel"><h2>Eventos recentes</h2><div class="events" id="events"></div></section>
</main>
<script>
const tokenInput=document.getElementById('token');
tokenInput.value=localStorage.getItem('appguard_token')||'';
function saveToken(){localStorage.setItem('appguard_token',tokenInput.value);refresh()}
function headers(){return {'Authorization':'Bearer '+tokenInput.value}}
async function api(path,options={}){options.headers={...(options.headers||{}),...headers()};const r=await fetch(path,options);if(!r.ok)throw new Error(await r.text());if(r.status===204)return null;return r.json()}
async function commandAgent(id,action){await api('/api/v1/admin/agent/'+encodeURIComponent(id)+'/'+action,{method:'POST'});setTimeout(refresh,500)}
async function broadcast(action){if(!confirm('Executar '+action+' em todos os agentes?'))return;await api('/api/v1/admin/broadcast/'+action,{method:'POST'});setTimeout(refresh,500)}
function esc(s){return String(s??'').replace(/[&<>"]/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c]))}
async function refresh(){
try{
const [agents,events]=await Promise.all([api('/api/v1/admin/agents'),api('/api/v1/admin/events')]);
document.getElementById('total').textContent=agents.length;
document.getElementById('online').textContent=agents.filter(a=>a.online).length;
document.getElementById('offline').textContent=agents.filter(a=>!a.online).length;
let running=0;
for(const a of agents){for(const p of (a.status?.apps||[]))if(p.running)running++}
document.getElementById('running').textContent=running;
document.getElementById('agents').innerHTML=agents.sort((a,b)=>a.agentId.localeCompare(b.agentId)).map(a=>{
const apps=(a.status?.apps||[]);const runningCount=apps.filter(p=>p.running).length;
return '<tr><td><strong>'+esc(a.agentId)+'</strong></td><td>'+esc(a.group)+'</td><td><span class="badge '+(a.online?'online':'')+'"><span class="dot"></span>'+(a.online?'Online':'Offline')+'</span></td><td>'+runningCount+' / '+apps.length+'</td><td>'+new Date(a.lastSeen).toLocaleString()+'</td><td><button class="btn" onclick="commandAgent(\\''+esc(a.agentId)+'\\',\\'restart-all\\')">Reiniciar</button> <button class="btn warn" onclick="commandAgent(\\''+esc(a.agentId)+'\\',\\'stop-all\\')">Parar</button></td></tr>'
}).join('');
document.getElementById('events').innerHTML=events.slice(0,100).map(e=>'<div class="event"><strong>'+esc(e.action)+'</strong> <span class="muted">'+new Date(e.time).toLocaleString()+'</span><br>'+esc(e.agentId||e.group||'global')+' — '+esc(e.detail||'')+'</div>').join('');
}catch(e){console.error(e)}
}
refresh();setInterval(refresh,5000);
</script>
</body>
</html>`
