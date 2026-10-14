package control

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"appguard/internal/config"
)

const (
	sessionCookie = "appguard_session"
	sessionTTL    = 12 * time.Hour
)

// authGuard protects the dashboard and admin API with a single operator
// login. Agent-facing endpoints (heartbeat, commands, events, enroll,
// update) stay open so agents never need credentials.
type authGuard struct {
	user    string
	pass    string
	enabled bool

	mu       sync.Mutex
	sessions map[string]time.Time // token -> expiry
}

func newAuthGuard(cfg config.Auth) *authGuard {
	user := firstNonEmpty(os.Getenv("APPGUARD_ADMIN_USER"), cfg.Username)
	pass := firstNonEmpty(os.Getenv("APPGUARD_ADMIN_PASSWORD"), cfg.Password)
	return &authGuard{
		user:     user,
		pass:     pass,
		enabled:  user != "" && pass != "",
		sessions: make(map[string]time.Time),
	}
}

// check reports whether credentials match in constant time.
func (a *authGuard) check(user, pass string) bool {
	u := subtle.ConstantTimeCompare([]byte(user), []byte(a.user))
	p := subtle.ConstantTimeCompare([]byte(pass), []byte(a.pass))
	return u == 1 && p == 1
}

func (a *authGuard) newSession() (string, time.Time) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	token := base64.RawURLEncoding.EncodeToString(b)
	expiry := time.Now().Add(sessionTTL)

	a.mu.Lock()
	a.pruneLocked()
	a.sessions[token] = expiry
	a.mu.Unlock()
	return token, expiry
}

func (a *authGuard) validSession(token string) bool {
	if token == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	expiry, ok := a.sessions[token]
	if !ok {
		return false
	}
	if time.Now().After(expiry) {
		delete(a.sessions, token)
		return false
	}
	return true
}

func (a *authGuard) dropSession(token string) {
	a.mu.Lock()
	delete(a.sessions, token)
	a.mu.Unlock()
}

func (a *authGuard) pruneLocked() {
	now := time.Now()
	for t, e := range a.sessions {
		if now.After(e) {
			delete(a.sessions, t)
		}
	}
}

func (a *authGuard) authenticated(r *http.Request) bool {
	if !a.enabled {
		return true
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	return a.validSession(c.Value)
}

// protectAPI wraps an admin API handler. Unauthenticated requests get 401.
func (a *authGuard) protectAPI(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.authenticated(r) {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// protectPage wraps an HTML page handler. Unauthenticated requests are
// redirected to the login page.
func (a *authGuard) protectPage(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.authenticated(r) {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

func (a *authGuard) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !a.enabled {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if r.Method == http.MethodGet {
		if a.authenticated(r) {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(loginHTML))
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var creds struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&creds); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if !a.check(strings.TrimSpace(creds.Username), creds.Password) {
		http.Error(w, "credenciais inválidas", http.StatusUnauthorized)
		return
	}

	token, expiry := a.newSession()
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		Expires:  expiry,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, map[string]any{"ok": true})
}

func (a *authGuard) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		a.dropSession(c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

const loginHTML = `<!doctype html>
<html lang="pt"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>AppGuard — Entrar</title>
<style>
body{font-family:Inter,system-ui,-apple-system,Segoe UI,sans-serif;background:linear-gradient(135deg,#07111f,#0c1d34 55%,#07111f);color:#e8edf7;margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center}
.box{background:#0d1c2d;border:1px solid #20344e;border-radius:16px;padding:34px;width:320px;box-shadow:0 12px 40px #0006}
h1{font-size:20px;margin:0 0 4px}.sub{color:#8fa5bf;font-size:13px;margin-bottom:22px}
label{display:block;font-size:12px;color:#8fa5bf;margin:14px 0 5px}
input{width:100%;background:#07111f;border:1px solid #2b4564;color:#fff;border-radius:8px;padding:11px;box-sizing:border-box}
button{width:100%;margin-top:22px;background:#1f6feb;color:#fff;border:0;border-radius:9px;padding:12px;font-size:15px;cursor:pointer}
button:hover{background:#2d7dfb}.err{color:#ff8a8a;font-size:13px;min-height:18px;margin-top:12px}
</style></head><body>
<form class="box" onsubmit="return login(event)">
<h1>AppGuard</h1><div class="sub">Centro de controlo</div>
<label>Utilizador</label><input id="u" autocomplete="username" autofocus>
<label>Palavra-passe</label><input id="p" type="password" autocomplete="current-password">
<button type="submit">Entrar</button>
<div class="err" id="err"></div>
</form>
<script>
async function login(e){e.preventDefault();const err=document.getElementById('err');err.textContent='';
try{const r=await fetch('/login',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({username:document.getElementById('u').value,password:document.getElementById('p').value})});
if(r.ok){location.href='/'}else{err.textContent=await r.text()}}catch(_){err.textContent='Erro de ligação'}return false}
</script></body></html>`
