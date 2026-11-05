// Package oauth is a small OAuth 2.1 authorization server for MCP clients:
// discovery (RFC 8414, RFC 9728), dynamic client registration (RFC 7591),
// PKCE, refresh and revocation. Sign-in is handed to an Upstream, and only
// allowlisted emails get tokens.
package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"go.icco.me/foods/internal/store"
	"go.icco.me/gutil/logging"
	"go.uber.org/zap"
	"golang.org/x/oauth2"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Scope is the only scope this server grants.
const Scope = "foods"

// Lifetimes. Refresh tokens are never rotated and each use extends them, so
// Gemini stays linked (Google calls this persistent linking).
const (
	AccessTTL   = time.Hour
	RefreshTTL  = 90 * 24 * time.Hour
	codeTTL     = 5 * time.Minute
	requestTTL  = 10 * time.Minute
	stateCookie = "foods_auth"
)

// DefaultRedirectHosts are where Gemini and Claude send people back to.
var DefaultRedirectHosts = []string{
	"oauth-redirect.googleusercontent.com",
	"oauth-redirect-sandbox.googleusercontent.com",
	"oauth-redirect-test.googleusercontent.com",
	"claude.ai",
	"claude.com",
}

// Server is the authorization server and the MCP token verifier.
type Server struct {
	DB            *gorm.DB
	Users         *store.Store
	Upstream      Upstream
	BaseURL       string
	AllowedEmails []string
	RedirectHosts []string
	Now           func() time.Time
}

// Migrate creates the OAuth tables.
func (s *Server) Migrate(ctx context.Context) error {
	return s.DB.WithContext(ctx).AutoMigrate(&Client{}, &AuthRequest{}, &AuthCode{}, &Token{})
}

// ResourceURL is the MCP endpoint these tokens are for.
func (s *Server) ResourceURL() string { return s.BaseURL + "/mcp" }

// ResourceMetadataURL is where the MCP endpoint's metadata lives.
func (s *Server) ResourceMetadataURL() string {
	return s.BaseURL + "/.well-known/oauth-protected-resource/mcp"
}

// Routes mounts every OAuth endpoint.
func (s *Server) Routes(r chi.Router) {
	prm := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth.ProtectedResourceMetadataHandler(&oauthex.ProtectedResourceMetadata{
			Resource: s.ResourceURL(), AuthorizationServers: []string{s.BaseURL}, ScopesSupported: []string{Scope},
			BearerMethodsSupported: []string{"header"}, ResourceName: "foods",
		}).ServeHTTP(w, r)
	})
	r.Method(http.MethodGet, "/.well-known/oauth-protected-resource", prm)
	r.Method(http.MethodGet, "/.well-known/oauth-protected-resource/mcp", prm)
	r.Get("/.well-known/oauth-authorization-server", s.metadata)
	r.Post("/register", s.register)
	r.Get("/authorize", s.authorize)
	r.Get("/callback", s.callback)
	r.Post("/token", s.token)
	r.Post("/revoke", s.revoke)
}

func (s *Server) metadata(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                         s.BaseURL,
		"authorization_endpoint":                         s.BaseURL + "/authorize",
		"token_endpoint":                                 s.BaseURL + "/token",
		"registration_endpoint":                          s.BaseURL + "/register",
		"revocation_endpoint":                            s.BaseURL + "/revoke",
		"response_types_supported":                       []string{"code"},
		"grant_types_supported":                          []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":               []string{"S256"},
		"token_endpoint_auth_methods_supported":          []string{"none", "client_secret_post", "client_secret_basic"},
		"revocation_endpoint_auth_methods_supported":     []string{"none", "client_secret_post", "client_secret_basic"},
		"scopes_supported":                               []string{Scope},
		"authorization_response_iss_parameter_supported": true,
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func oauthError(w http.ResponseWriter, code int, kind, desc string) {
	writeJSON(w, code, map[string]string{"error": kind, "error_description": desc})
}

func secret() string { return rand.Text() + rand.Text() }

func hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func (s *Server) allowedRedirect(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Fragment != "" || u.User != nil {
		return false
	}
	switch u.Scheme {
	case "https":
		return slices.Contains(s.RedirectHosts, u.Hostname())
	case "http":
		h := u.Hostname()
		return h == "localhost" || net.ParseIP(h).IsLoopback()
	}
	return false
}

func (s *Server) allowedEmail(email string) bool {
	return slices.ContainsFunc(s.AllowedEmails, func(e string) bool { return strings.EqualFold(e, email) })
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RedirectURIs []string `json:"redirect_uris"`
		ClientName   string   `json:"client_name"`
		AuthMethod   string   `json:"token_endpoint_auth_method"`
		GrantTypes   []string `json:"grant_types"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "Send JSON client metadata.")
		return
	}
	if len(in.RedirectURIs) == 0 || len(in.RedirectURIs) > 10 {
		oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "Send 1 to 10 redirect_uris.")
		return
	}
	for _, u := range in.RedirectURIs {
		if !s.allowedRedirect(u) {
			oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "This server only redirects to known MCP clients and loopback addresses: "+u)
			return
		}
	}
	if in.AuthMethod == "" {
		in.AuthMethod = "client_secret_basic"
	}
	if !slices.Contains([]string{"none", "client_secret_post", "client_secret_basic"}, in.AuthMethod) {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "Unsupported token_endpoint_auth_method.")
		return
	}
	if len(in.ClientName) > 200 {
		in.ClientName = in.ClientName[:200]
	}
	c := &Client{ID: rand.Text(), Name: in.ClientName, RedirectURIs: in.RedirectURIs, AuthMethod: in.AuthMethod, CreatedAt: s.Now()}
	out := map[string]any{
		"client_id": c.ID, "client_id_issued_at": c.CreatedAt.Unix(), "client_name": c.Name, "redirect_uris": c.RedirectURIs,
		"token_endpoint_auth_method": c.AuthMethod, "grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"},
	}
	if c.AuthMethod != "none" {
		sec := secret()
		h := hash(sec)
		c.SecretHash = &h
		out["client_secret"], out["client_secret_expires_at"] = sec, 0
	}
	if err := s.DB.WithContext(r.Context()).Create(c).Error; err != nil {
		s.fail(w, r, "register client", err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	logging.FromContext(r.Context()).Errorw(what, zap.Error(err))
	oauthError(w, http.StatusInternalServerError, "server_error", "Something went wrong. Try again.")
}

func (s *Server) client(ctx context.Context, id string) (*Client, error) {
	var c Client
	res := s.DB.WithContext(ctx).Where("id = ?", id).Limit(1).Find(&c)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, nil
	}
	return &c, nil
}

func redirectWith(w http.ResponseWriter, r *http.Request, to string, params url.Values) {
	u, _ := url.Parse(to)
	q := u.Query()
	for k, v := range params {
		q[k] = v
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound) //nolint:gosec // callers pass only redirect URIs checked against the client registration
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	c, err := s.client(r.Context(), q.Get("client_id"))
	if err != nil {
		s.fail(w, r, "load client", err)
		return
	}
	i := -1
	if c != nil {
		i = slices.Index(c.RedirectURIs, q.Get("redirect_uri"))
	}
	if i < 0 {
		http.Error(w, "Unknown client_id or redirect_uri. Reconnect the app to register again.", http.StatusBadRequest)
		return
	}
	redirect := c.RedirectURIs[i]
	back := func(kind, desc string) {
		redirectWith(w, r, redirect, url.Values{"error": {kind}, "error_description": {desc}, "state": {q.Get("state")}, "iss": {s.BaseURL}})
	}
	switch {
	case q.Get("response_type") != "code":
		back("unsupported_response_type", "Only response_type=code is supported.")
		return
	case q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256":
		back("invalid_request", "PKCE with code_challenge_method=S256 is required.")
		return
	case q.Get("resource") != "" && q.Get("resource") != s.ResourceURL() && q.Get("resource") != s.BaseURL:
		back("invalid_target", "Unknown resource.")
		return
	}
	now := s.Now()
	s.DB.WithContext(r.Context()).Where("expires_at < ?", now).Delete(&AuthRequest{})
	ar := &AuthRequest{ID: rand.Text(), ClientID: c.ID, RedirectURI: redirect, State: q.Get("state"), CodeChallenge: q.Get("code_challenge"), UpstreamVerifier: oauth2.GenerateVerifier(), ExpiresAt: now.Add(requestTTL)}
	if err := s.DB.WithContext(r.Context()).Create(ar).Error; err != nil {
		s.fail(w, r, "save auth request", err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: stateCookie, Value: ar.ID, Path: "/callback", MaxAge: int(requestTTL.Seconds()), HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, s.Upstream.AuthCodeURL(ar.ID, ar.UpstreamVerifier), http.StatusFound)
}

func (s *Server) callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	cookie, err := r.Cookie(stateCookie)
	if err != nil || q.Get("state") == "" || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(q.Get("state"))) != 1 {
		http.Error(w, "This sign-in link expired or was opened in another browser. Start again from the app.", http.StatusBadRequest)
		return
	}
	var ar AuthRequest
	res := s.DB.WithContext(r.Context()).Clauses(clause.Returning{}).Where("id = ? AND expires_at > ?", q.Get("state"), s.Now()).Delete(&ar)
	if res.Error != nil {
		s.fail(w, r, "load auth request", res.Error)
		return
	}
	if res.RowsAffected == 0 {
		http.Error(w, "This sign-in link expired. Start again from the app.", http.StatusBadRequest)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: stateCookie, Path: "/callback", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
	back := func(params url.Values) {
		params.Set("state", ar.State)
		params.Set("iss", s.BaseURL)
		redirectWith(w, r, ar.RedirectURI, params)
	}
	if q.Get("error") != "" {
		back(url.Values{"error": {"access_denied"}, "error_description": {"Google sign-in was cancelled."}})
		return
	}
	id, err := s.Upstream.Exchange(r.Context(), q.Get("code"), ar.UpstreamVerifier)
	if err != nil {
		s.fail(w, r, "google sign-in", err)
		return
	}
	if !id.EmailVerified || !s.allowedEmail(id.Email) {
		logging.FromContext(r.Context()).Warnw("sign-in refused", "email", id.Email)
		back(url.Values{"error": {"access_denied"}, "error_description": {"This Google account may not use this server."}})
		return
	}
	u, err := s.Users.FindOrCreateUser(r.Context(), "google", id.Subject, strings.ToLower(id.Email))
	if err != nil {
		s.fail(w, r, "find user", err)
		return
	}
	code := secret()
	ac := &AuthCode{Hash: hash(code), ClientID: ar.ClientID, UserID: u.ID, RedirectURI: ar.RedirectURI, CodeChallenge: ar.CodeChallenge, ExpiresAt: s.Now().Add(codeTTL)}
	if err := s.DB.WithContext(r.Context()).Create(ac).Error; err != nil {
		s.fail(w, r, "save code", err)
		return
	}
	back(url.Values{"code": {code}})
}

// authClient checks the client's credentials on /token and /revoke.
func (s *Server) authClient(r *http.Request) (*Client, error) {
	id, sec, basic := r.BasicAuth()
	if basic {
		var err error
		if id, err = url.QueryUnescape(id); err != nil {
			return nil, err
		}
		if sec, err = url.QueryUnescape(sec); err != nil {
			return nil, err
		}
	} else {
		id, sec = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	c, err := s.client(r.Context(), id)
	if err != nil || c == nil {
		return nil, errors.Join(errors.New("unknown client"), err)
	}
	if c.SecretHash == nil {
		return c, nil
	}
	if sec == "" || subtle.ConstantTimeCompare([]byte(hash(sec)), []byte(*c.SecretHash)) != 1 {
		return nil, errors.New("bad client secret")
	}
	return c, nil
}

func pkceOK(verifier, challenge string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	return subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(sum[:])), []byte(challenge)) == 1
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "Send a form-encoded body.")
		return
	}
	c, err := s.authClient(r)
	if err != nil {
		oauthError(w, http.StatusUnauthorized, "invalid_client", "Client authentication failed.")
		return
	}
	ctx := r.Context()
	now := s.Now()
	s.DB.WithContext(ctx).Where("expires_at < ?", now).Delete(&AuthCode{})
	s.DB.WithContext(ctx).Where("expires_at < ?", now).Delete(&Token{})

	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		var ac AuthCode
		res := s.DB.WithContext(ctx).Clauses(clause.Returning{}).Where("hash = ?", hash(r.PostForm.Get("code"))).Delete(&ac)
		if res.Error != nil {
			s.fail(w, r, "load code", res.Error)
			return
		}
		if res.RowsAffected == 0 || ac.ClientID != c.ID || ac.ExpiresAt.Before(now) || ac.RedirectURI != r.PostForm.Get("redirect_uri") {
			oauthError(w, http.StatusBadRequest, "invalid_grant", "The code is unknown, used, expired, or for another client or redirect_uri.")
			return
		}
		if !pkceOK(r.PostForm.Get("code_verifier"), ac.CodeChallenge) {
			oauthError(w, http.StatusBadRequest, "invalid_grant", "code_verifier does not match code_challenge.")
			return
		}
		refresh := secret()
		grant := rand.Text()
		var access string
		err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(&Token{Hash: hash(refresh), Kind: kindRefresh, ClientID: c.ID, UserID: ac.UserID, GrantID: grant, ExpiresAt: now.Add(RefreshTTL), CreatedAt: now}).Error; err != nil {
				return err
			}
			var err error
			access, err = s.newAccess(tx, c.ID, ac.UserID, grant)
			return err
		})
		if err != nil {
			s.fail(w, r, "issue tokens", err)
			return
		}
		writeTokens(w, access, refresh)
	case "refresh_token":
		refresh := r.PostForm.Get("refresh_token")
		var access string
		// Locking the refresh token makes a concurrent revoke wait, so the revoke also sees the access token issued here.
		err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var t Token
			res := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("hash = ? AND kind = ? AND client_id = ? AND expires_at > ?", hash(refresh), kindRefresh, c.ID, now).Limit(1).Find(&t)
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				return errRevoked
			}
			u, err := s.Users.GetUser(ctx, t.UserID)
			if err != nil || !s.allowedEmail(u.Email) {
				return errNotAllowed
			}
			if err := tx.Model(&t).Update("expires_at", now.Add(RefreshTTL)).Error; err != nil {
				return err
			}
			access, err = s.newAccess(tx, c.ID, t.UserID, t.GrantID)
			return err
		})
		switch {
		case errors.Is(err, errRevoked):
			oauthError(w, http.StatusBadRequest, "invalid_grant", "The refresh token is unknown, revoked or expired.")
		case errors.Is(err, errNotAllowed):
			oauthError(w, http.StatusBadRequest, "invalid_grant", "This account may no longer use this server.")
		case err != nil:
			s.fail(w, r, "refresh", err)
		default:
			writeTokens(w, access, refresh)
		}
	default:
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type", "Use authorization_code or refresh_token.")
	}
}

var (
	errRevoked    = errors.New("refresh token unknown, revoked or expired")
	errNotAllowed = errors.New("user not allowed")
)

func (s *Server) newAccess(tx *gorm.DB, clientID, userID, grant string) (string, error) {
	access := secret()
	now := s.Now()
	return access, tx.Create(&Token{Hash: hash(access), Kind: kindAccess, ClientID: clientID, UserID: userID, GrantID: grant, ExpiresAt: now.Add(AccessTTL), CreatedAt: now}).Error
}

func writeTokens(w http.ResponseWriter, access, refresh string) {
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": access, "token_type": "Bearer", "expires_in": int(AccessTTL.Seconds()), "refresh_token": refresh, "scope": Scope,
	})
}

// revoke ends a token. Revoking a refresh token ends its whole grant.
func (s *Server) revoke(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "Send a form-encoded body.")
		return
	}
	c, err := s.authClient(r)
	if err != nil {
		oauthError(w, http.StatusUnauthorized, "invalid_client", "Client authentication failed.")
		return
	}
	err = s.DB.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		var t Token
		res := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("hash = ? AND client_id = ?", hash(r.PostForm.Get("token")), c.ID).Limit(1).Find(&t)
		if res.Error != nil || res.RowsAffected == 0 {
			return res.Error
		}
		if t.Kind == kindRefresh {
			return tx.Where("grant_id = ?", t.GrantID).Delete(&Token{}).Error
		}
		return tx.Where("hash = ?", t.Hash).Delete(&Token{}).Error
	})
	if err != nil {
		s.fail(w, r, "revoke token", err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// Verify checks a bearer token on /mcp.
func (s *Server) Verify(ctx context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
	var t Token
	res := s.DB.WithContext(ctx).Where("hash = ? AND kind = ? AND expires_at > ?", hash(token), kindAccess, s.Now()).Limit(1).Find(&t)
	if res.Error != nil {
		return nil, fmt.Errorf("load token: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return nil, auth.ErrInvalidToken
	}
	u, err := s.Users.GetUser(ctx, t.UserID)
	if err != nil || !s.allowedEmail(u.Email) {
		return nil, fmt.Errorf("%w: user not allowed", auth.ErrInvalidToken)
	}
	return &auth.TokenInfo{UserID: t.UserID, Scopes: []string{Scope}, Expiration: t.ExpiresAt}, nil
}
