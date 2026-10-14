package control

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"appguard/internal/config"
	"appguard/internal/storage"
)

const offlineAfter = 20 * time.Second

type controllerState struct {
	mu    sync.RWMutex
	token string
	db    *storage.DB
}

type agentView struct {
	AgentID   string         `json:"agentId"`
	Group     string         `json:"group"`
	LastSeen  time.Time      `json:"lastSeen"`
	Online    bool           `json:"online"`
	Status    map[string]any `json:"status"`
}

func RunController(cfg *config.Config) error {
	db, err := storage.Open(cfg.Controller.DatabasePath)
	if err != nil {
		return fmt.Errorf("open controller database: %w", err)
	}
	defer db.Close()

	st := &controllerState{
		token: cfg.Controller.Token,
		db: db,
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"status": "ok",
			"service": "appguard-controller",
			"time": time.Now().UTC(),
		})
	})

	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := st.db.Ping(); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, map[string]any{
			"status": "ready",
			"database": "ok",
		})
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(dashboardHTML))
	})

	mux.HandleFunc("/agent/", func(w http.ResponseWriter, r *http.Request) {
		agentID := strings.TrimPrefix(r.URL.Path, "/agent/")
		if agentID == "" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		page := strings.ReplaceAll(agentHTML, "{{AGENT_ID}}", templateJS(agentID))
		_, _ = w.Write([]byte(page))
	})

	mux.HandleFunc("/api/v1/enroll", st.auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req Enrollment
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.AgentID == "" {
			http.Error(w, "missing agentId", http.StatusBadRequest)
			return
		}
		if req.Group == "" {
			req.Group = "default"
		}

		status := map[string]any{
			"version": req.Version,
			"enrolled": true,
			"apps": []any{},
		}
		if err := st.db.UpsertAgent(req.AgentID, req.Group, time.Now(), status); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		_ = st.db.AddEvent(storage.Event{
			Time: time.Now(),
			AgentID: req.AgentID,
			Group: req.Group,
			Action: "agent-enrolled",
			Detail: "Agente registado manualmente no controlador",
		})
		writeJSON(w, map[string]any{
			"enrolled": true,
			"agentId": req.AgentID,
			"version": req.Version,
		})
	}))

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
		if hb.Status == nil {
			hb.Status = map[string]any{}
		}
		hb.Status["version"] = hb.Version
		hb.Status["settings"] = hb.Settings

		previous, previousErr := st.db.GetAgent(hb.AgentID)
		if err := st.db.UpsertAgent(hb.AgentID, hb.Group, hb.Timestamp, hb.Status); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if previousErr == sql.ErrNoRows {
			_ = st.db.AddEvent(storage.Event{
				Time: time.Now(), AgentID: hb.AgentID, Group: hb.Group,
				Action: "agent-online", Detail: "Agente registado no controlador",
			})
		} else if previous != nil && time.Since(previous.LastSeen) > offlineAfter {
			_ = st.db.AddEvent(storage.Event{
				Time: time.Now(), AgentID: hb.AgentID, Group: hb.Group,
				Action: "agent-reconnected", Detail: "Agente voltou a comunicar com o controlador",
			})
		}

		w.WriteHeader(http.StatusNoContent)
	}))

	mux.HandleFunc("/api/v1/agent-events", st.auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var e Event
		if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if e.Action == "" {
			http.Error(w, "missing action", http.StatusBadRequest)
			return
		}

		if err := st.db.AddEvent(storage.Event{
			Time: e.Time,
			AgentID: e.AgentID,
			Group: e.Group,
			Action: e.Action,
			AppName: e.AppName,
			Detail: e.Detail,
		}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	mux.HandleFunc("/api/v1/commands/", st.auth(func(w http.ResponseWriter, r *http.Request) {
		agentID := strings.TrimPrefix(r.URL.Path, "/api/v1/commands/")
		if agentID == "" {
			http.Error(w, "missing agent id", http.StatusBadRequest)
			return
		}

		stored, err := st.db.FetchPendingCommands(agentID, 50)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		cmds := make([]Command, 0, len(stored))
		for _, item := range stored {
			cmds = append(cmds, Command{
				ID: item.ID,
				Action: item.Action,
				AppName: item.AppName,
				Payload: item.Payload,
			})
		}
		writeJSON(w, cmds)
	}))

	mux.HandleFunc("/api/v1/commands/ack/", st.auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		commandID := strings.TrimPrefix(r.URL.Path, "/api/v1/commands/ack/")
		if commandID == "" {
			http.Error(w, "missing command id", http.StatusBadRequest)
			return
		}
		if err := st.db.AckCommand(commandID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	mux.HandleFunc("/api/v1/admin/agents", st.auth(func(w http.ResponseWriter, r *http.Request) {
		agents, err := st.db.ListAgents()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		now := time.Now()
		result := make([]agentView, 0, len(agents))
		for _, a := range agents {
			result = append(result, agentView{
				AgentID: a.AgentID,
				Group: a.Group,
				LastSeen: a.LastSeen,
				Online: now.Sub(a.LastSeen) <= offlineAfter,
				Status: a.Status,
			})
		}
		writeJSON(w, result)
	}))

	mux.HandleFunc("/api/v1/admin/agent-detail/", st.auth(func(w http.ResponseWriter, r *http.Request) {
		agentID := strings.TrimPrefix(r.URL.Path, "/api/v1/admin/agent-detail/")
		if agentID == "" {
			http.Error(w, "missing agent id", http.StatusBadRequest)
			return
		}

		a, err := st.db.GetAgent(agentID)
		if err == sql.ErrNoRows {
			http.Error(w, "unknown agent", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		events, err := st.db.ListEvents(agentID, 200)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		writeJSON(w, map[string]any{
			"agent": agentView{
				AgentID: a.AgentID,
				Group: a.Group,
				LastSeen: a.LastSeen,
				Online: time.Since(a.LastSeen) <= offlineAfter,
				Status: a.Status,
			},
			"events": events,
		})
	}))

	mux.HandleFunc("/api/v1/admin/events", st.auth(func(w http.ResponseWriter, r *http.Request) {
		agentID := r.URL.Query().Get("agentId")
		events, err := st.db.ListEvents(agentID, 300)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
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

		agents, err := st.db.ListAgents()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		st.mu.Lock()
		for _, a := range agents {
			st.enqueueLocked(a.AgentID, action, "")
		}
		st.mu.Unlock()

		_ = st.db.AddEvent(storage.Event{
			Time: time.Now(), Action: action, Detail: "Comando global enviado",
		})
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
		a, err := st.db.GetAgent(agentID)
		if err == sql.ErrNoRows {
			http.Error(w, "unknown agent", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		st.mu.Lock()
		st.enqueueLocked(agentID, action, "")
		st.mu.Unlock()

		_ = st.db.AddEvent(storage.Event{
			Time: time.Now(), AgentID: agentID, Group: a.Group,
			Action: action, Detail: "Comando enviado ao agente",
		})

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
		agents, err := st.db.ListAgents()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		count := 0
		st.mu.Lock()
		for _, a := range agents {
			if a.Group == group {
				st.enqueueLocked(a.AgentID, action, "")
				count++
			}
		}
		st.mu.Unlock()

		_ = st.db.AddEvent(storage.Event{
			Time: time.Now(), Group: group, Action: action,
			Detail: fmt.Sprintf("Comando enviado a %d agente(s)", count),
		})

		writeJSON(w, map[string]any{"accepted": count})
	}))

	mux.HandleFunc("/api/v1/admin/agent-settings/", st.auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		agentID := strings.TrimPrefix(r.URL.Path, "/api/v1/admin/agent-settings/")
		if agentID == "" {
			http.Error(w, "missing agent id", http.StatusBadRequest)
			return
		}
		if _, err := st.db.GetAgent(agentID); err == sql.ErrNoRows {
			http.Error(w, "unknown agent", http.StatusNotFound)
			return
		} else if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		var settings config.AgentSettings
		if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		config.NormalizeSettings(&settings)

		payload, err := json.Marshal(settings)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if err := st.db.QueueCommand(storage.Command{
			ID: fmt.Sprintf("%d-%s", time.Now().UnixNano(), agentID),
			AgentID: agentID,
			Action: "apply-settings",
			Payload: payload,
			Created: time.Now(),
		}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		_ = st.db.AddEvent(storage.Event{
			Time: time.Now(), AgentID: agentID, Group: settings.Group,
			Action: "apply-settings", Detail: "Configuração remota colocada na fila",
		})

		writeJSON(w, map[string]any{"queued": true})
	}))

	slog.Info("AppGuard controller listening",
		"addr", cfg.Controller.Listen,
		"database", cfg.Controller.DatabasePath)

	server := &http.Server{
		Addr: cfg.Controller.Listen,
		Handler: mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	return server.ListenAndServe()
}

func (s *controllerState) enqueueLocked(agentID, action, appName string) {
	_ = s.db.QueueCommand(storage.Command{
		ID: fmt.Sprintf("%d-%s", time.Now().UnixNano(), agentID),
		AgentID: agentID,
		Action: action,
		AppName: appName,
		Created: time.Now(),
	})
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

func templateJS(value string) string {
	return strings.ReplaceAll(url.QueryEscape(value), "+", "%20")
}

const dashboardHTML = `<!doctype html>
<html lang="pt">
<head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>AppGuard Control Center</title>
<style>
:root{font-family:Inter,ui-sans-serif,system-ui,-apple-system,Segoe UI,sans-serif;color:#e8edf7;background:#07111f}
*{box-sizing:border-box}body{margin:0;background:linear-gradient(135deg,#07111f,#0c1d34 55%,#07111f);min-height:100vh}
header{padding:24px 30px;border-bottom:1px solid #20344e;background:#091522e8;position:sticky;top:0;z-index:3}
h1{font-size:22px;margin:0}.sub{color:#8fa5bf;font-size:13px;margin-top:4px}
main{padding:26px 30px;max-width:1500px;margin:auto}.cards{display:grid;grid-template-columns:repeat(5,minmax(130px,1fr));gap:14px;margin-bottom:22px}
.card,.panel{background:#0d1c2d;border:1px solid #20344e;border-radius:14px;box-shadow:0 8px 28px #0004}
.card{padding:18px}.metric{font-size:30px;font-weight:700}.label{font-size:11px;color:#8fa5bf;text-transform:uppercase;letter-spacing:.08em}
.toolbar{display:flex;gap:9px;flex-wrap:wrap;margin:16px 0}.btn{background:#183451;color:#fff;border:1px solid #2a4b6b;border-radius:9px;padding:8px 12px;cursor:pointer}.btn:hover{background:#214665}
.btn.warn{background:#5b2c22;border-color:#824034}.btn.good{background:#164733;border-color:#246849}
.panel{overflow:hidden;margin-bottom:20px}.panel h2{font-size:15px;margin:0;padding:15px 18px;border-bottom:1px solid #20344e}
table{width:100%;border-collapse:collapse}th,td{text-align:left;padding:12px 14px;border-bottom:1px solid #172b42;font-size:13px}th{color:#8fa5bf}
.badge{display:inline-flex;align-items:center;gap:7px;padding:5px 8px;border-radius:999px;background:#17304a}.dot{width:8px;height:8px;border-radius:50%;background:#ef5d5d}.online .dot{background:#46d887}
.events{max-height:330px;overflow:auto}.event{padding:10px 16px;border-bottom:1px solid #172b42;font-size:12px;color:#b9c8dc}.muted{color:#70869f}
input,select,textarea{background:#07111f;border:1px solid #2b4564;color:#fff;border-radius:8px;padding:9px 11px}input{min-width:240px}textarea{width:100%;min-height:260px;font-family:Consolas,monospace}a{color:#8fc7ff;text-decoration:none}
@media(max-width:900px){.cards{grid-template-columns:1fr 1fr}main,header{padding-left:15px;padding-right:15px}table{display:block;overflow:auto}}
</style>
</head>
<body>
<header><h1>AppGuard Control Center <span class="muted">v1.0.0</span></h1><div class="sub">Supervisão central e histórico persistente de processos Windows</div></header>
<main>
<div class="toolbar"><input id="token" type="password" placeholder="Token Bearer"><button class="btn" onclick="saveToken()">Guardar token</button></div>
<div class="toolbar"><input id="search" placeholder="Pesquisar computador ou grupo" oninput="renderAgents()"><select id="groupFilter" onchange="renderAgents()"><option value="">Todos os grupos</option></select><select id="stateFilter" onchange="renderAgents()"><option value="">Todos os estados</option><option value="online">Online</option><option value="offline">Offline</option></select></div>
<div class="cards">
<div class="card"><div class="label">Computadores</div><div class="metric" id="total">0</div></div>
<div class="card"><div class="label">Online</div><div class="metric" id="online">0</div></div>
<div class="card"><div class="label">Offline</div><div class="metric" id="offline">0</div></div>
<div class="card"><div class="label">Processos activos</div><div class="metric" id="running">0</div></div>
<div class="card"><div class="label">Falhas recentes</div><div class="metric" id="failures">0</div></div>
</div>
<div class="toolbar">
<button class="btn good" onclick="broadcast('enable-all')">Activar todos</button>
<button class="btn" onclick="broadcast('restart-all')">Reiniciar todos</button>
<button class="btn warn" onclick="broadcast('stop-all')">Parar todos</button>
<button class="btn warn" onclick="broadcast('disable-all')">Desactivar watchdog</button>
</div>
<section class="panel"><h2>Agentes</h2>
<table><thead><tr><th>Computador</th><th>Grupo</th><th>Versão</th><th>Estado</th><th>Processos</th><th>Retries</th><th>Último contacto</th><th>Acções</th></tr></thead><tbody id="agents"></tbody></table>
</section>
<section class="panel"><h2>Eventos persistentes</h2><div class="events" id="events"></div></section>
</main>
<script>
let cachedAgents=[]; const tokenInput=document.getElementById('token');tokenInput.value=localStorage.getItem('appguard_token')||'';
function saveToken(){localStorage.setItem('appguard_token',tokenInput.value);refresh()}
function headers(){return {'Authorization':'Bearer '+tokenInput.value}}
async function api(path,options={}){options.headers={...(options.headers||{}),...headers()};const r=await fetch(path,options);if(!r.ok)throw new Error(await r.text());if(r.status===204)return null;return r.json()}
async function commandAgent(id,action){await api('/api/v1/admin/agent/'+encodeURIComponent(id)+'/'+action,{method:'POST'});setTimeout(refresh,500)}
async function broadcast(action){if(!confirm('Executar '+action+' em todos os agentes?'))return;await api('/api/v1/admin/broadcast/'+action,{method:'POST'});setTimeout(refresh,500)}
function esc(s){return String(s??'').replace(/[&<>"]/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c]))}
function renderAgents(){
const q=(document.getElementById('search')?.value||'').toLowerCase();
const group=document.getElementById('groupFilter')?.value||'';
const state=document.getElementById('stateFilter')?.value||'';
const agents=cachedAgents.filter(a=>(!q||(a.agentId+' '+a.group).toLowerCase().includes(q))&&(!group||a.group===group)&&(!state||(state==='online')===a.online));
document.getElementById('agents').innerHTML=agents.map(a=>{const apps=a.status?.apps||[];const rc=apps.filter(p=>p.running).length;const rt=apps.reduce((n,p)=>n+Number(p.retries||0),0);return '<tr><td><a href="/agent/'+encodeURIComponent(a.agentId)+'"><strong>'+esc(a.agentId)+'</strong></a></td><td>'+esc(a.group)+'</td><td>'+esc(a.status?.version||'-')+'</td><td><span class="badge '+(a.online?'online':'')+'"><span class="dot"></span>'+(a.online?'Online':'Offline')+'</span></td><td>'+rc+' / '+apps.length+'</td><td>'+rt+'</td><td>'+new Date(a.lastSeen).toLocaleString()+'</td><td><button class="btn" onclick="commandAgent(\\''+esc(a.agentId)+'\\',\\'restart-all\\')">Reiniciar</button> <button class="btn warn" onclick="commandAgent(\\''+esc(a.agentId)+'\\',\\'stop-all\\')">Parar</button></td></tr>'}).join('');
}
async function refresh(){try{
const [agents,events]=await Promise.all([api('/api/v1/admin/agents'),api('/api/v1/admin/events')]);
document.getElementById('total').textContent=agents.length;
document.getElementById('online').textContent=agents.filter(a=>a.online).length;
document.getElementById('offline').textContent=agents.filter(a=>!a.online).length;
let running=0,retries=0;for(const a of agents){for(const p of (a.status?.apps||[])){if(p.running)running++;retries+=Number(p.retries||0)}}
document.getElementById('running').textContent=running;
document.getElementById('failures').textContent=events.filter(e=>['process-crash','restart-failed','retry-limit-reached'].includes(e.action)).length;
cachedAgents=agents;
const groups=[...new Set(agents.map(a=>a.group))].sort();
const gf=document.getElementById('groupFilter'), oldGroup=gf.value;
gf.innerHTML='<option value="">Todos os grupos</option>'+groups.map(g=>'<option value="'+esc(g)+'">'+esc(g)+'</option>').join('');
gf.value=oldGroup;
renderAgents();
document.getElementById('events').innerHTML=events.slice(0,120).map(e=>'<div class="event"><strong>'+esc(e.action)+'</strong> '+(e.appName?'· '+esc(e.appName):'')+' <span class="muted">'+new Date(e.time).toLocaleString()+'</span><br>'+esc(e.agentId||e.group||'global')+' — '+esc(e.detail||'')+'</div>').join('');
}catch(e){console.error(e)}}
refresh();setInterval(refresh,5000);
</script></body></html>`

const agentHTML = `<!doctype html>
<html lang="pt"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>AppGuard Agent</title>
<style>
body{font-family:Inter,system-ui;background:#07111f;color:#e8edf7;margin:0}.wrap{max-width:1100px;margin:auto;padding:28px}.panel{background:#0d1c2d;border:1px solid #20344e;border-radius:14px;margin:16px 0;overflow:hidden}.panel h2{font-size:16px;padding:14px 18px;margin:0;border-bottom:1px solid #20344e}.body{padding:18px}.btn{background:#183451;color:#fff;border:1px solid #2a4b6b;border-radius:9px;padding:8px 12px;cursor:pointer}.warn{background:#5b2c22}.good{background:#164733}.muted{color:#8fa5bf}table{width:100%;border-collapse:collapse}th,td{padding:11px;border-bottom:1px solid #172b42;text-align:left;font-size:13px}input,textarea{background:#07111f;color:#fff;border:1px solid #2b4564;border-radius:8px;padding:9px}textarea{width:100%;min-height:300px;font-family:Consolas,monospace}a{color:#8fc7ff}
</style></head><body><div class="wrap"><a href="/">← Dashboard</a><h1 id="title">Agente</h1>
<div><input id="token" type="password" placeholder="Token Bearer"> <button class="btn" onclick="saveToken()">Guardar token</button></div>
<div class="panel"><h2>Estado</h2><div class="body" id="summary">A carregar...</div></div>
<div class="panel"><h2>Processos</h2><div class="body"><table><thead><tr><th>Aplicação</th><th>Estado</th><th>Retries</th><th>PIDs</th></tr></thead><tbody id="apps"></tbody></table></div></div>
<div class="panel"><h2>Configuração remota</h2><div class="body"><p class="muted">Altere apenas grupo, intervalo e aplicações. Não são executados comandos shell.</p><textarea id="settings"></textarea><br><br><button class="btn good" onclick="saveSettings()">Enviar configuração</button></div></div>
<div class="panel"><h2>Histórico</h2><div class="body" id="events"></div></div>
</div><script>
const id=decodeURIComponent('{{AGENT_ID}}');const token=document.getElementById('token');token.value=localStorage.getItem('appguard_token')||'';document.getElementById('title').textContent='Agente '+id;
function saveToken(){localStorage.setItem('appguard_token',token.value);refresh()}
async function api(path,opts={}){opts.headers={...(opts.headers||{}),'Authorization':'Bearer '+token.value};const r=await fetch(path,opts);if(!r.ok)throw new Error(await r.text());if(r.status===204)return null;return r.json()}
function esc(s){return String(s??'').replace(/[&<>"]/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c]))}
async function command(action){await api('/api/v1/admin/agent/'+encodeURIComponent(id)+'/'+action,{method:'POST'});setTimeout(refresh,500)}
async function saveSettings(){let body;try{body=JSON.parse(document.getElementById('settings').value)}catch(e){alert('JSON inválido');return}await api('/api/v1/admin/agent-settings/'+encodeURIComponent(id),{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});alert('Configuração enviada para o agente')}
async function refresh(){try{const d=await api('/api/v1/admin/agent-detail/'+encodeURIComponent(id));const a=d.agent;document.getElementById('summary').innerHTML='<b>'+esc(a.agentId)+'</b> · '+esc(a.group)+' · '+(a.online?'Online':'Offline')+' · último contacto '+new Date(a.lastSeen).toLocaleString()+'<br><br><button class="btn good" onclick="command(\\'enable-all\\')">Activar</button> <button class="btn" onclick="command(\\'restart-all\\')">Reiniciar</button> <button class="btn warn" onclick="command(\\'stop-all\\')">Parar</button> <button class="btn warn" onclick="command(\\'disable-all\\')">Desactivar</button>';document.getElementById('apps').innerHTML=(a.status?.apps||[]).map(p=>'<tr><td>'+esc(p.name)+'</td><td>'+(p.running?'Running':(p.restartPending?'Retry pendente':'Stopped'))+'</td><td>'+Number(p.retries||0)+' / '+Number(p.maxRetries||0)+'</td><td>'+esc((p.pids||[]).join(', '))+'</td></tr>').join('');const ta=document.getElementById('settings');if(ta&&!ta.matches(':focus'))ta.value=JSON.stringify(a.status?.settings||{},null,2);document.getElementById('events').innerHTML=d.events.map(e=>'<p><b>'+esc(e.action)+'</b> '+(e.appName?'· '+esc(e.appName):'')+' <span class="muted">'+new Date(e.time).toLocaleString()+'</span><br>'+esc(e.detail||'')+'</p>').join('')}catch(e){document.getElementById('summary').textContent=e.message}}
refresh();setInterval(refresh,5000);
</script></body></html>`
