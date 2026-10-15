// Package auth signs people in with Google (OpenID Connect, authorization-code
// flow, no SDK) and keeps them signed in with a server-side session: the
// cookie holds a random token, the store holds only its hash.
//
//	GET  /auth/google            → Google's consent screen (state in a cookie)
//	GET  /auth/google/callback   → exchange the code, read the verified email,
//	                               start a session, back to where they were going
//	POST /auth/logout            → end the session, 303 to the landing page
//	POST /auth/dev               → a local-only sign-in by email (-dev-login)
package auth

import (
	"sync"
	"whiteboard/ack"

	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/starfederation/datastar-go/datastar"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"whiteboard/db"
)

const (
	sessionCookie = "wb_session"
	stateCookie   = "wb_oauth"
)

type Auth struct {
	DB           *db.DB
	ClientID     string
	ClientSecret string
	BaseURL      string // https://draw.deufel.dev — the redirect URI is built from it
	Dev          bool   // allow /auth/dev: local testing without Google

	sessions sync.Map // token → cached: a session's user, for a moment
}

// cached is a session looked up a moment ago. Every request carries the
// cookie (every pointer move); the user behind it changes only at sign-out,
// which forgets it here at once.
type cached struct {
	u  *db.User
	at time.Time
}

const sessionCacheFor = 30 * time.Second

func (a *Auth) sessionUser(ctx context.Context, token string) (*db.User, error) {
	if v, ok := a.sessions.Load(token); ok {
		if c := v.(cached); time.Since(c.at) < sessionCacheFor {
			return c.u, nil
		}
	}
	u, err := a.DB.SessionUser(ctx, token)
	if err == nil && u != nil {
		a.sessions.Store(token, cached{u, time.Now()})
	} else if err == nil {
		a.sessions.Delete(token)
	}
	return u, err
}

// Google reports whether Google sign-in is configured.
func (a *Auth) Google() bool { return a.ClientID != "" && a.ClientSecret != "" }

type ctxKey struct{}

// Middleware puts the signed-in user (or nil) on every request's context.
func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(sessionCookie); err == nil {
			u, err := a.sessionUser(r.Context(), c.Value)
			if err != nil {
				slog.Error("session", "err", err)
			}
			if u != nil {
				r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, u))
			}
		}
		next.ServeHTTP(w, r)
	})
}

// User is the signed-in user, or nil.
func User(ctx context.Context) *db.User {
	u, _ := ctx.Value(ctxKey{}).(*db.User)
	return u
}

// SignInURL is where to send someone who must sign in first.
func SignInURL(next string) string { return "/?next=" + url.QueryEscape(next) }

// safeNext keeps a post-sign-in destination on this site.
func safeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		return "/"
	}
	return next
}

func (a *Auth) secure(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" || strings.HasPrefix(a.BaseURL, "https://")
}

// Start sends the browser to Google's consent screen.
func (a *Auth) Start(w http.ResponseWriter, r *http.Request) {
	if !a.Google() {
		http.Error(w, "Google sign-in is not configured", http.StatusServiceUnavailable)
		return
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		http.Error(w, "sign-in unavailable", http.StatusInternalServerError)
		return
	}
	state := base64.RawURLEncoding.EncodeToString(raw)
	next := safeNext(r.URL.Query().Get("next"))
	http.SetCookie(w, &http.Cookie{
		Name: stateCookie, Value: state + "|" + url.QueryEscape(next), Path: "/auth",
		MaxAge: 600, HttpOnly: true, Secure: a.secure(r), SameSite: http.SameSiteLaxMode,
	})
	q := url.Values{
		"client_id":     {a.ClientID},
		"redirect_uri":  {a.BaseURL + "/auth/google/callback"},
		"response_type": {"code"},
		"scope":         {"openid email profile"},
		"state":         {state},
		"prompt":        {"select_account"},
	}
	http.Redirect(w, r, "https://accounts.google.com/o/oauth2/v2/auth?"+q.Encode(), http.StatusFound)
}

// Callback finishes the Google sign-in.
func (a *Auth) Callback(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(stateCookie)
	state, nextEnc, _ := strings.Cut(func() string {
		if err != nil {
			return ""
		}
		return c.Value
	}(), "|")
	if state == "" || r.URL.Query().Get("state") != state {
		http.Error(w, "sign-in expired: please try again", http.StatusBadRequest)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: stateCookie, Path: "/auth", MaxAge: -1})
	if e := r.URL.Query().Get("error"); e != "" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	info, err := a.exchange(r.Context(), r.URL.Query().Get("code"))
	if err != nil {
		slog.Error("google sign-in", "err", err)
		http.Error(w, "sign-in failed: please try again", http.StatusBadGateway)
		return
	}
	if !info.EmailVerified || info.Email == "" {
		http.Error(w, "your Google account's email is not verified", http.StatusForbidden)
		return
	}
	u, err := a.DB.UpsertUser(r.Context(), "google:"+info.Sub, info.Email, info.Name, info.Picture)
	if err != nil {
		slog.Error("user", "err", err)
		http.Error(w, "sign-in failed", http.StatusInternalServerError)
		return
	}
	next, _ := url.QueryUnescape(nextEnc)
	if a.session(w, r, u.ID) { // Google navigated here: a real redirect on
		http.Redirect(w, r, safeNext(next), http.StatusSeeOther)
	}
}

type userinfo struct {
	Sub           string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
	Picture       string `json:"picture"`
}

// exchange trades the code for an access token, then reads the userinfo.
func (a *Auth) exchange(ctx context.Context, code string) (userinfo, error) {
	var info userinfo
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	form := url.Values{
		"code":          {code},
		"client_id":     {a.ClientID},
		"client_secret": {a.ClientSecret},
		"redirect_uri":  {a.BaseURL + "/auth/google/callback"},
		"grant_type":    {"authorization_code"},
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://oauth2.googleapis.com/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return info, err
	}
	defer res.Body.Close()
	var tok struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(res.Body).Decode(&tok); err != nil {
		return info, err
	}
	if tok.AccessToken == "" {
		return info, fmt.Errorf("token exchange: %s (%d)", tok.Error, res.StatusCode)
	}
	req, _ = http.NewRequestWithContext(ctx, http.MethodGet, "https://openidconnect.googleapis.com/v1/userinfo", nil)
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	res2, err := http.DefaultClient.Do(req)
	if err != nil {
		return info, err
	}
	defer res2.Body.Close()
	if res2.StatusCode != http.StatusOK {
		return info, fmt.Errorf("userinfo: %d", res2.StatusCode)
	}
	return info, json.NewDecoder(res2.Body).Decode(&info)
}

// session starts a session and sets its cookie; on false it has answered.
func (a *Auth) session(w http.ResponseWriter, r *http.Request, userID int64) bool {
	token, expires, err := a.DB.NewSession(r.Context(), userID)
	if err != nil {
		slog.Error("session", "err", err)
		http.Error(w, "sign-in failed", http.StatusInternalServerError)
		return false
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", Expires: expires,
		HttpOnly: true, Secure: a.secure(r), SameSite: http.SameSiteLaxMode,
	})
	return true
}

// Logout ends this device's session (the others stay signed in). A Datastar
// post: the cookie goes in the headers, then the stream sends the page home.
func (a *Auth) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		a.sessions.Delete(c.Value)
		_ = a.DB.EndSession(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1, HttpOnly: true, Secure: a.secure(r), SameSite: http.SameSiteLaxMode})
	_ = datastar.NewSSE(w, r).Redirect("/")
}

// Dev signs in by email alone. It exists only with -dev-login, for testing
// accounts and invites locally without Google. A Datastar post of $email and
// $next.
func (a *Auth) DevLogin(w http.ResponseWriter, r *http.Request) {
	if !a.Dev {
		http.NotFound(w, r)
		return
	}
	var s struct {
		Email string `json:"email"`
		Next  string `json:"next"`
	}
	if err := datastar.ReadSignals(r, &s); err != nil {
		http.Error(w, "bad signals", http.StatusBadRequest)
		return
	}
	email := strings.ToLower(strings.TrimSpace(s.Email))
	if email == "" || !strings.Contains(email, "@") {
		ack.Bad(datastar.NewSSE(w, r), "email", "An email, please")
		return
	}
	name, _, _ := strings.Cut(email, "@")
	u, err := a.DB.UpsertUser(r.Context(), "dev:"+email, email, strings.ToUpper(name[:1])+name[1:], "")
	if err != nil {
		http.Error(w, "sign-in failed", http.StatusInternalServerError)
		return
	}
	if a.session(w, r, u.ID) {
		_ = datastar.NewSSE(w, r).Redirect(safeNext(s.Next))
	}
}
