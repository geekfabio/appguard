package control

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"appguard/internal/config"
)

func TestAuthGuardDisabledWhenNoCredentials(t *testing.T) {
	g := newAuthGuard(config.Auth{})
	if g.enabled {
		t.Fatal("guard must be disabled without credentials")
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if !g.authenticated(req) {
		t.Fatal("disabled guard must allow all requests")
	}
}

func TestAuthGuardSessionLifecycle(t *testing.T) {
	g := newAuthGuard(config.Auth{Username: "admin", Password: "s3cr3t"})
	if !g.enabled {
		t.Fatal("guard should be enabled")
	}
	if g.check("admin", "wrong") || g.check("x", "s3cr3t") {
		t.Fatal("wrong credentials accepted")
	}
	if !g.check("admin", "s3cr3t") {
		t.Fatal("correct credentials rejected")
	}

	token, _ := g.newSession()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if g.authenticated(req) {
		t.Fatal("request without cookie must not be authenticated")
	}
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	if !g.authenticated(req) {
		t.Fatal("valid session rejected")
	}
	g.dropSession(token)
	if g.authenticated(req) {
		t.Fatal("dropped session still valid")
	}
}

func TestProtectAPIAndPage(t *testing.T) {
	g := newAuthGuard(config.Auth{Username: "a", Password: "b"})
	hit := false
	next := func(w http.ResponseWriter, r *http.Request) { hit = true }

	rec := httptest.NewRecorder()
	g.protectAPI(next)(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/agents", nil))
	if rec.Code != http.StatusUnauthorized || hit {
		t.Fatalf("API without session: code=%d hit=%v", rec.Code, hit)
	}

	rec = httptest.NewRecorder()
	g.protectPage(next)(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("page redirect: code=%d loc=%s", rec.Code, rec.Header().Get("Location"))
	}

	token, _ := g.newSession()
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/agents", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	g.protectAPI(next)(rec, req)
	if !hit {
		t.Fatal("authenticated request blocked")
	}
}

func TestLoginFlow(t *testing.T) {
	g := newAuthGuard(config.Auth{Username: "admin", Password: "pw"})
	rec := httptest.NewRecorder()
	g.handleLogin(rec, httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"admin","password":"pw"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("login code = %d", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Set-Cookie"), sessionCookie) {
		t.Fatal("no session cookie set")
	}

	rec = httptest.NewRecorder()
	g.handleLogin(rec, httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"admin","password":"bad"}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad login code = %d", rec.Code)
	}
}

func TestEnvOverridesConfigCredentials(t *testing.T) {
	t.Setenv("APPGUARD_ADMIN_USER", "envuser")
	t.Setenv("APPGUARD_ADMIN_PASSWORD", "envpass")
	g := newAuthGuard(config.Auth{Username: "cfg", Password: "cfg"})
	if !g.check("envuser", "envpass") || g.check("cfg", "cfg") {
		t.Fatal("environment credentials should take precedence")
	}
}
