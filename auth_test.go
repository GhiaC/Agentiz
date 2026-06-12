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
