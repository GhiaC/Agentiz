package agentize

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ghiac/agentize/log"
)

const (
	adminCookieName = "agentize_admin"
	adminSessionTTL = 24 * time.Hour
	adminLoginPath  = "/agentize/login"
)

// SetAdminCredentials sets the admin username/password that protect the
// /agentize web pages (docs, graph, debug). /agentize/health stays open for
// health checks; Prometheus metrics are served separately on a dedicated port
// (see AGENTIZE_METRICS_ADDR), not on these pages. Call before RegisterRoutes.
// When never called, the AGENTIZE_ADMIN_USERNAME / AGENTIZE_ADMIN_PASSWORD
// environment variables are used; if those are empty too, auth is disabled.
func (ag *Agentize) SetAdminCredentials(username, password string) {
	ag.adminUsername = username
	ag.adminPassword = password
}

// initAdminAuth resolves credentials from the environment when not set
// programmatically, and warns when the dashboard ends up unprotected.
func (ag *Agentize) initAdminAuth() {
	if ag.adminUsername == "" && ag.adminPassword == "" {
		ag.adminUsername = os.Getenv("AGENTIZE_ADMIN_USERNAME")
		ag.adminPassword = os.Getenv("AGENTIZE_ADMIN_PASSWORD")
	}
	if !ag.adminAuthEnabled() {
		log.Log.Warnf("[Agentize] ⚠️  Admin auth DISABLED — /agentize pages are open. Set AGENTIZE_ADMIN_USERNAME and AGENTIZE_ADMIN_PASSWORD (or call SetAdminCredentials) to protect them")
	}
}

func (ag *Agentize) adminAuthEnabled() bool {
	return ag.adminUsername != "" && ag.adminPassword != ""
}

// adminKey derives the HMAC key for session tokens from the credentials, so
// changing the password invalidates every outstanding session.
func (ag *Agentize) adminKey() []byte {
	h := sha256.Sum256([]byte("agentize-admin-v1:" + ag.adminUsername + ":" + ag.adminPassword))
	return h[:]
}

// signAdminToken builds a "expiry|signature" token valid until expiry (unix seconds).
func (ag *Agentize) signAdminToken(expiry int64) string {
	payload := strconv.FormatInt(expiry, 10)
	mac := hmac.New(sha256.New, ag.adminKey())
	mac.Write([]byte(payload))
	return payload + "|" + hex.EncodeToString(mac.Sum(nil))
}

func (ag *Agentize) verifyAdminToken(token string) bool {
	parts := strings.SplitN(token, "|", 2)
	if len(parts) != 2 {
		return false
	}
	expiry, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || time.Now().Unix() > expiry {
		return false
	}
	expected := ag.signAdminToken(expiry)
	return subtle.ConstantTimeCompare([]byte(token), []byte(expected)) == 1
}

func (ag *Agentize) isAdminRequest(c *gin.Context) bool {
	cookie, err := c.Cookie(adminCookieName)
	return err == nil && ag.verifyAdminToken(cookie)
}

// requireAdmin wraps a handler with the admin session check. Browsers are
// redirected to the login page; API-ish requests get a plain 401.
func (ag *Agentize) requireAdmin(h gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !ag.adminAuthEnabled() || ag.isAdminRequest(c) {
			h(c)
			return
		}
		if strings.Contains(c.GetHeader("Accept"), "text/html") {
			c.Redirect(http.StatusFound, adminLoginPath+"?next="+url.QueryEscape(c.Request.URL.RequestURI()))
			return
		}
		c.Header("WWW-Authenticate", `Bearer realm="agentize"`)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
	}
}

// safeNextTarget only allows redirecting back inside /agentize, preventing
// open-redirect via the ?next= parameter.
func safeNextTarget(next string) string {
	if strings.HasPrefix(next, "/agentize") && !strings.HasPrefix(next, "//") && !strings.Contains(next, "\\") {
		return next
	}
	return "/agentize"
}

// handleLoginPage renders the login form (or redirects if already signed in).
func (ag *Agentize) handleLoginPage(c *gin.Context) {
	if !ag.adminAuthEnabled() || ag.isAdminRequest(c) {
		c.Redirect(http.StatusFound, safeNextTarget(c.Query("next")))
		return
	}
	ag.renderLoginPage(c, c.Query("next"), false)
}

// handleLoginSubmit validates the credentials and issues the session cookie.
func (ag *Agentize) handleLoginSubmit(c *gin.Context) {
	if !ag.adminAuthEnabled() {
		c.Redirect(http.StatusFound, "/agentize")
		return
	}
	username := c.PostForm("username")
	password := c.PostForm("password")
	next := c.PostForm("next")

	// Hash both sides before comparing so timing reveals neither value length.
	userOK := constantTimeEqualHashed(username, ag.adminUsername)
	passOK := constantTimeEqualHashed(password, ag.adminPassword)
	if !userOK || !passOK {
		log.Log.Warnf("[Agentize] 🔐 Failed admin login attempt | IP: %s", c.ClientIP())
		ag.renderLoginPage(c, next, true)
		return
	}

	expiry := time.Now().Add(adminSessionTTL).Unix()
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(adminCookieName, ag.signAdminToken(expiry), int(adminSessionTTL.Seconds()), "/agentize", "", false, true)
	log.Log.Infof("[Agentize] 🔐 Admin login | IP: %s", c.ClientIP())
	c.Redirect(http.StatusFound, safeNextTarget(next))
}

// handleLogout clears the admin session cookie.
func (ag *Agentize) handleLogout(c *gin.Context) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(adminCookieName, "", -1, "/agentize", "", false, true)
	c.Redirect(http.StatusFound, adminLoginPath)
}

func constantTimeEqualHashed(a, b string) bool {
	ha := sha256.Sum256([]byte(a))
	hb := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}

func (ag *Agentize) renderLoginPage(c *gin.Context, next string, failed bool) {
	errorHTML := ""
	if failed {
		errorHTML = `<div class="error">Invalid username or password</div>`
	}
	html := fmt.Sprintf(loginPageTemplate, errorHTML, htmlEscape(next))
	status := http.StatusOK
	if failed {
		status = http.StatusUnauthorized
	}
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(status, html)
}

func htmlEscape(s string) string {
	return strings.NewReplacer(
		"&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;",
	).Replace(s)
}

const loginPageTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
	<meta charset="UTF-8"/>
	<meta name="viewport" content="width=device-width, initial-scale=1.0"/>
	<title>Sign in · Agentize</title>
	<style>
		:root {
			--bg: #f3f5fa; --surface: #ffffff; --border: #e4e8f1;
			--text: #1a2233; --text-2: #5b6478; --text-3: #8a93a8;
			--accent: #4f6df5; --accent-soft: rgba(79,109,245,0.10);
			--danger: #dc2626; --danger-soft: #fdecec;
		}
		@media (prefers-color-scheme: dark) {
			:root {
				--bg: #0e1117; --surface: #161b26; --border: #262e40;
				--text: #e6e9f0; --text-2: #9aa3b5; --text-3: #6b7385;
				--accent: #7c93ff; --accent-soft: rgba(124,147,255,0.14);
				--danger: #f87171; --danger-soft: rgba(248,113,113,0.12);
			}
		}
		* { margin: 0; padding: 0; box-sizing: border-box; }
		body {
			font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Arial, sans-serif;
			background: var(--bg); color: var(--text);
			min-height: 100vh; display: flex; align-items: center; justify-content: center; padding: 24px;
		}
		.card {
			background: var(--surface); border: 1px solid var(--border); border-radius: 14px;
			box-shadow: 0 10px 40px rgba(16,24,40,0.08);
			width: 100%%; max-width: 380px; padding: 36px 32px;
		}
		.logo { font-size: 34px; text-align: center; margin-bottom: 10px; }
		h1 { font-size: 19px; text-align: center; margin-bottom: 4px; }
		.sub { color: var(--text-3); font-size: 13px; text-align: center; margin-bottom: 24px; }
		label { display: block; font-size: 12.5px; font-weight: 600; color: var(--text-2); margin: 14px 0 5px; }
		input {
			width: 100%%; padding: 10px 12px; font-size: 14px;
			border: 1px solid var(--border); border-radius: 9px;
			background: var(--bg); color: var(--text); outline: none;
			transition: border-color .15s ease, box-shadow .15s ease;
		}
		input:focus { border-color: var(--accent); box-shadow: 0 0 0 3px var(--accent-soft); }
		button {
			width: 100%%; margin-top: 22px; padding: 11px;
			background: var(--accent); color: #fff; font-size: 14px; font-weight: 600;
			border: none; border-radius: 9px; cursor: pointer;
		}
		button:hover { filter: brightness(1.07); }
		.error {
			background: var(--danger-soft); color: var(--danger);
			border-radius: 9px; padding: 10px 12px; font-size: 13px;
			text-align: center; margin-bottom: 6px;
		}
		.foot { text-align: center; margin-top: 18px; font-size: 12px; color: var(--text-3); }
	</style>
</head>
<body>
	<form class="card" method="POST" action="/agentize/login">
		<div class="logo">🧠</div>
		<h1>Agentize Admin</h1>
		<div class="sub">Sign in to access the dashboard</div>
		%s
		<label for="username">Username</label>
		<input id="username" name="username" type="text" autocomplete="username" autofocus required/>
		<label for="password">Password</label>
		<input id="password" name="password" type="password" autocomplete="current-password" required/>
		<input type="hidden" name="next" value="%s"/>
		<button type="submit">Sign in</button>
		<div class="foot">Protected area — sessions expire after 24h</div>
	</form>
</body>
</html>`
