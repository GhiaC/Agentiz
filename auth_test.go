package agentize

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func newAuthTestRouter(ag *Agentize) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/agentize/login", ag.handleLoginPage)
	router.POST("/agentize/login", ag.handleLoginSubmit)
	router.GET("/agentize/logout", ag.handleLogout)
	router.GET("/agentize/protected", ag.requireAdmin(func(c *gin.Context) {
		c.String(200, "secret")
	}))
	return router
}

func TestRequireAdmin_DisabledAllowsAccess(t *testing.T) {
	ag := &Agentize{} // no credentials configured
	router := newAuthTestRouter(ag)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/agentize/protected", nil)
	router.ServeHTTP(w, req)

	if w.Code != 200 || w.Body.String() != "secret" {
		t.Fatalf("expected open access without credentials, got %d %q", w.Code, w.Body.String())
	}
}

// TestRegisterRoutes_SafeByDefault: with no credentials and no AGENTIZE_DEBUG_UNSAFE,
// the dashboard routes are NOT registered at all (the conversations/delete surface
// is not exposed open by default), while /agentize/health stays available.
func TestRegisterRoutes_SafeByDefault(t *testing.T) {
	t.Setenv("AGENTIZE_ADMIN_USERNAME", "")
	t.Setenv("AGENTIZE_ADMIN_PASSWORD", "")
	t.Setenv("AGENTIZE_DEBUG_UNSAFE", "")

	ag := &Agentize{}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	ag.RegisterRoutes(router)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/agentize/debug", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("dashboard must not be registered without creds/UNSAFE, got %d", w.Code)
	}
	wh := httptest.NewRecorder()
	router.ServeHTTP(wh, httptest.NewRequest("GET", "/agentize/health", nil))
	if wh.Code == http.StatusNotFound {
		t.Errorf("/agentize/health must remain registered, got %d", wh.Code)
	}
}

// TestRegisterRoutes_WithCredsRegistersGated: with credentials, the dashboard IS
// registered but gated — an unauthenticated browser is redirected to login.
func TestRegisterRoutes_WithCredsRegistersGated(t *testing.T) {
	t.Setenv("AGENTIZE_DEBUG_UNSAFE", "")
	ag := &Agentize{adminUsername: "admin", adminPassword: "pw"}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	ag.RegisterRoutes(router)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/agentize/debug", nil)
	req.Header.Set("Accept", "text/html")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusFound {
		t.Errorf("with creds, /agentize/debug should redirect to login (302), got %d", w.Code)
	}
}

// TestLogin_CookieSecureFollowsHTTPS: the session cookie is Secure over HTTPS
// (incl. behind a TLS-terminating proxy) and not Secure over plain HTTP, so the
// token never travels in cleartext in production while local dev still works.
func TestLogin_CookieSecureFollowsHTTPS(t *testing.T) {
	ag := &Agentize{adminUsername: "admin", adminPassword: "s3cret"}
	router := newAuthTestRouter(ag)

	login := func(https bool) string {
		form := url.Values{"username": {"admin"}, "password": {"s3cret"}}
		req := httptest.NewRequest("POST", "/agentize/login", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if https {
			req.Header.Set("X-Forwarded-Proto", "https")
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w.Header().Get("Set-Cookie")
	}

	if sc := login(true); !strings.Contains(sc, "Secure") || !strings.Contains(sc, "HttpOnly") {
		t.Errorf("over HTTPS the cookie must be Secure + HttpOnly, got %q", sc)
	}
	if sc := login(false); strings.Contains(sc, "Secure") {
		t.Errorf("over plain HTTP the cookie must NOT be Secure (local dev), got %q", sc)
	}
}

func TestRequireAdmin_RedirectsBrowserToLogin(t *testing.T) {
	ag := &Agentize{adminUsername: "admin", adminPassword: "s3cret"}
	router := newAuthTestRouter(ag)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/agentize/protected", nil)
	req.Header.Set("Accept", "text/html")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("expected 302 redirect, got %d", w.Code)
	}
	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, "/agentize/login?next=") {
		t.Fatalf("expected redirect to login with next param, got %q", loc)
	}
}

func TestRequireAdmin_NonBrowserGets401(t *testing.T) {
	ag := &Agentize{adminUsername: "admin", adminPassword: "s3cret"}
	router := newAuthTestRouter(ag)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/agentize/protected", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for non-HTML request, got %d", w.Code)
	}
}

func TestLogin_WrongPasswordRejected(t *testing.T) {
	ag := &Agentize{adminUsername: "admin", adminPassword: "s3cret"}
	router := newAuthTestRouter(ag)

	form := url.Values{"username": {"admin"}, "password": {"wrong"}}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/agentize/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 on wrong password, got %d", w.Code)
	}
	if len(w.Result().Cookies()) != 0 {
		t.Fatal("no session cookie should be set on failed login")
	}
}

func TestLogin_SuccessSetsCookieAndGrantsAccess(t *testing.T) {
	ag := &Agentize{adminUsername: "admin", adminPassword: "s3cret"}
	router := newAuthTestRouter(ag)

	form := url.Values{"username": {"admin"}, "password": {"s3cret"}, "next": {"/agentize/protected"}}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/agentize/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("expected 302 after login, got %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/agentize/protected" {
		t.Fatalf("expected redirect to next target, got %q", loc)
	}

	var sessionCookie *http.Cookie
	for _, ck := range w.Result().Cookies() {
		if ck.Name == adminCookieName {
			sessionCookie = ck
		}
	}
	if sessionCookie == nil {
		t.Fatal("expected admin session cookie on successful login")
	}
	if !sessionCookie.HttpOnly {
		t.Error("session cookie must be HttpOnly")
	}

	// Cookie grants access to the protected page.
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "/agentize/protected", nil)
	req2.AddCookie(sessionCookie)
	router.ServeHTTP(w2, req2)
	if w2.Code != 200 || w2.Body.String() != "secret" {
		t.Fatalf("expected access with session cookie, got %d %q", w2.Code, w2.Body.String())
	}
}

func TestLogin_OpenRedirectBlocked(t *testing.T) {
	ag := &Agentize{adminUsername: "admin", adminPassword: "s3cret"}
	router := newAuthTestRouter(ag)

	form := url.Values{"username": {"admin"}, "password": {"s3cret"}, "next": {"https://evil.example.com"}}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/agentize/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	router.ServeHTTP(w, req)

	if loc := w.Header().Get("Location"); loc != "/agentize" {
		t.Fatalf("external next target must fall back to /agentize, got %q", loc)
	}
}

func TestAdminToken_ExpiryAndTamper(t *testing.T) {
	ag := &Agentize{adminUsername: "admin", adminPassword: "s3cret"}

	valid := ag.signAdminToken(time.Now().Add(time.Hour).Unix())
	if !ag.verifyAdminToken(valid) {
		t.Fatal("freshly signed token must verify")
	}

	expired := ag.signAdminToken(time.Now().Add(-time.Minute).Unix())
	if ag.verifyAdminToken(expired) {
		t.Fatal("expired token must not verify")
	}

	if ag.verifyAdminToken(valid+"x") || ag.verifyAdminToken("garbage") {
		t.Fatal("tampered token must not verify")
	}

	// Changing the password invalidates outstanding tokens.
	ag2 := &Agentize{adminUsername: "admin", adminPassword: "rotated"}
	if ag2.verifyAdminToken(valid) {
		t.Fatal("token signed with old password must not verify after rotation")
	}
}

func TestLogout_ClearsCookie(t *testing.T) {
	ag := &Agentize{adminUsername: "admin", adminPassword: "s3cret"}
	router := newAuthTestRouter(ag)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/agentize/logout", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("expected redirect after logout, got %d", w.Code)
	}
	cleared := false
	for _, ck := range w.Result().Cookies() {
		if ck.Name == adminCookieName && ck.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("logout must clear the session cookie")
	}
}
