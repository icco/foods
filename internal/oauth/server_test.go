package oauth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"go.icco.me/foods/internal/store"
	"go.icco.me/foods/internal/testdb"
	"golang.org/x/oauth2"
)

func TestMain(m *testing.M) { testdb.Main(m) }

// fakeGoogle signs in whoever is in next.
type fakeGoogle struct {
	base string
	next Identity
	fail bool
}

func (g *fakeGoogle) AuthCodeURL(state, _ string) string {
	return g.base + "/fake-google?state=" + url.QueryEscape(state)
}

func (g *fakeGoogle) Exchange(_ context.Context, code, verifier string) (*Identity, error) {
	if g.fail || code != "google-code" || verifier == "" {
		return nil, errors.New("bad exchange")
	}
	id := g.next
	return &id, nil
}

const geminiRedirect = "https://oauth-redirect.googleusercontent.com/r/foods"

type env struct {
	srv    *Server
	ts     *httptest.Server
	http   *http.Client
	google *fakeGoogle
	now    time.Time
}

func setup(t *testing.T) *env {
	t.Helper()
	db := testdb.Open(t)
	st := store.New(db)
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	e := &env{google: &fakeGoogle{next: Identity{Subject: uuid.NewString(), Email: "Alice@Example.com", EmailVerified: true}}, now: time.Now()}
	e.srv = &Server{DB: db, Users: st, Upstream: e.google, AllowedEmails: []string{"alice@example.com"}, RedirectHosts: DefaultRedirectHosts, Now: func() time.Time { return e.now }}
	if err := e.srv.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	e.srv.Routes(r)
	r.Get("/fake-google", func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, "/callback?code=google-code&state="+url.QueryEscape(req.URL.Query().Get("state")), http.StatusFound)
	})
	e.ts = httptest.NewTLSServer(r)
	t.Cleanup(e.ts.Close)
	e.srv.BaseURL, e.google.base = e.ts.URL, e.ts.URL
	jar, _ := cookiejar.New(nil)
	e.http = e.ts.Client()
	e.http.Jar = jar
	e.http.CheckRedirect = func(req *http.Request, _ []*http.Request) error {
		if strings.HasPrefix(req.URL.String(), e.ts.URL) {
			return nil
		}
		return http.ErrUseLastResponse
	}
	return e
}

func (e *env) register(t *testing.T, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, e.ts.URL+"/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res := e.do(t, req)
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

// authorize runs the browser part and returns the final redirect's query.
func (e *env) authorize(t *testing.T, params url.Values) url.Values {
	t.Helper()
	res, err := e.get(t, e.ts.URL+"/authorize?"+params.Encode())
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusFound {
		t.Fatalf("authorize ended with %d", res.StatusCode)
	}
	u, err := url.Parse(res.Header.Get("Location"))
	if err != nil || !strings.HasPrefix(u.String(), geminiRedirect) {
		t.Fatalf("redirected to %q", res.Header.Get("Location"))
	}
	return u.Query()
}

func (e *env) post(t *testing.T, path string, form url.Values, user, pass string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, e.ts.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if user != "" {
		req.SetBasicAuth(url.QueryEscape(user), url.QueryEscape(pass))
	}
	res := e.do(t, req)
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func challenge(v string) string {
	s := sha256.Sum256([]byte(v))
	return base64.RawURLEncoding.EncodeToString(s[:])
}

func TestFullFlow(t *testing.T) {
	e := setup(t)
	ctx := context.Background()

	res, err := e.get(t, e.ts.URL+"/.well-known/oauth-authorization-server")
	if err != nil {
		t.Fatal(err)
	}
	var meta map[string]any
	_ = json.NewDecoder(res.Body).Decode(&meta)
	if meta["registration_endpoint"] != e.ts.URL+"/register" || meta["issuer"] != e.ts.URL {
		t.Errorf("metadata = %v", meta)
	}
	res, _ = e.get(t, e.ts.URL+"/.well-known/oauth-protected-resource/mcp")
	var prm map[string]any
	_ = json.NewDecoder(res.Body).Decode(&prm)
	if prm["resource"] != e.ts.URL+"/mcp" || e.srv.ResourceMetadataURL() != e.ts.URL+"/.well-known/oauth-protected-resource/mcp" {
		t.Errorf("resource metadata = %v", prm)
	}

	code, client := e.register(t, `{"client_name":"Gemini","redirect_uris":["`+geminiRedirect+`"]}`)
	if code != http.StatusCreated || client["client_secret"] == nil {
		t.Fatalf("register = %d %v", code, client)
	}
	id, sec := client["client_id"].(string), client["client_secret"].(string)

	verifier := oauth2.GenerateVerifier()
	q := e.authorize(t, url.Values{"response_type": {"code"}, "client_id": {id}, "redirect_uri": {geminiRedirect}, "state": {"xyz"},
		"code_challenge": {challenge(verifier)}, "code_challenge_method": {"S256"}, "resource": {e.ts.URL + "/mcp"}})
	if q.Get("state") != "xyz" || q.Get("code") == "" || q.Get("iss") != e.ts.URL {
		t.Fatalf("callback query = %v", q)
	}

	form := url.Values{"grant_type": {"authorization_code"}, "code": {q.Get("code")}, "redirect_uri": {geminiRedirect}, "code_verifier": {"wrong-verifier-wrong-verifier-wrong-verifier-123"}}
	if st, out := e.post(t, "/token", form, id, sec); st != http.StatusBadRequest || out["error"] != "invalid_grant" {
		t.Fatalf("bad verifier = %d %v", st, out)
	}
	// A failed attempt burns the code.
	form.Set("code_verifier", verifier)
	if st, _ := e.post(t, "/token", form, id, sec); st != http.StatusBadRequest {
		t.Fatalf("reused code = %d", st)
	}

	q = e.authorize(t, url.Values{"response_type": {"code"}, "client_id": {id}, "redirect_uri": {geminiRedirect}, "code_challenge": {challenge(verifier)}, "code_challenge_method": {"S256"}})
	form.Set("code", q.Get("code"))
	if st, out := e.post(t, "/token", form, id, "wrong"); st != http.StatusUnauthorized || out["error"] != "invalid_client" {
		t.Fatalf("wrong secret = %d %v", st, out)
	}
	st, tok := e.post(t, "/token", form, id, sec)
	if st != http.StatusOK || tok["token_type"] != "Bearer" || tok["refresh_token"] == nil {
		t.Fatalf("token = %d %v", st, tok)
	}
	access, refresh := tok["access_token"].(string), tok["refresh_token"].(string)

	info, err := e.srv.Verify(ctx, access, nil)
	if err != nil || info.UserID == "" || info.Scopes[0] != Scope {
		t.Fatalf("verify = %+v, %v", info, err)
	}
	u, _ := e.srv.Users.GetUser(ctx, info.UserID)
	if u.Email != "alice@example.com" {
		t.Errorf("email = %q", u.Email)
	}
	if _, err := e.srv.Verify(ctx, "nope", nil); !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("unknown token: %v", err)
	}
	if _, err := e.srv.Verify(ctx, refresh, nil); !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("refresh token used as access token: %v", err)
	}

	// Refresh returns the same refresh token (persistent linking) and a new access token.
	st, again := e.post(t, "/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {id}, "client_secret": {sec}}, "", "")
	if st != http.StatusOK || again["refresh_token"] != refresh || again["access_token"] == access {
		t.Fatalf("refresh = %d %v", st, again)
	}

	e.now = e.now.Add(2 * AccessTTL)
	if _, err := e.srv.Verify(ctx, access, nil); !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("expired access token: %v", err)
	}
	if st, _ := e.post(t, "/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}}, id, sec); st != http.StatusOK {
		t.Errorf("refresh after access expiry = %d", st)
	}

	// Revoking the refresh token ends the grant.
	if st, _ := e.post(t, "/revoke", url.Values{"token": {refresh}}, id, sec); st != http.StatusOK {
		t.Fatalf("revoke = %d", st)
	}
	if st, _ := e.post(t, "/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}}, id, sec); st != http.StatusBadRequest {
		t.Errorf("refresh after revoke = %d", st)
	}
	if st, _ := e.post(t, "/revoke", url.Values{"token": {"unknown"}}, id, sec); st != http.StatusOK {
		t.Errorf("revoking an unknown token must still succeed: %d", st)
	}
	if st, _ := e.post(t, "/token", url.Values{"grant_type": {"password"}}, id, sec); st != http.StatusBadRequest {
		t.Errorf("password grant = %d", st)
	}

	// Dropping someone from the allowlist cuts them off.
	e.srv.AllowedEmails = nil
	q = e.authorize(t, url.Values{"response_type": {"code"}, "client_id": {id}, "redirect_uri": {geminiRedirect}, "code_challenge": {challenge(verifier)}, "code_challenge_method": {"S256"}})
	if q.Get("error") != "access_denied" {
		t.Errorf("not allowed = %v", q)
	}
}

func TestPublicClientAndRefusals(t *testing.T) {
	e := setup(t)
	code, client := e.register(t, `{"redirect_uris":["http://127.0.0.1:33418/cb"],"token_endpoint_auth_method":"none"}`)
	if code != http.StatusCreated || client["client_secret"] != nil {
		t.Fatalf("public client = %d %v", code, client)
	}
	for _, body := range []string{
		`not json`,
		`{"redirect_uris":[]}`,
		`{"redirect_uris":["https://evil.example/cb"]}`,
		`{"redirect_uris":["https://claude.ai/cb#x"]}`,
		`{"redirect_uris":["ftp://localhost/cb"]}`,
		`{"redirect_uris":["https://claude.ai/cb"],"token_endpoint_auth_method":"private_key_jwt"}`,
	} {
		if code, out := e.register(t, body); code != http.StatusBadRequest {
			t.Errorf("register %s = %d %v", body, code, out)
		}
	}

	_, gem := e.register(t, `{"redirect_uris":["`+geminiRedirect+`"]}`)
	id := gem["client_id"].(string)
	base := url.Values{"response_type": {"code"}, "client_id": {id}, "redirect_uri": {geminiRedirect}, "code_challenge": {"abc"}, "code_challenge_method": {"S256"}}
	for k, v := range map[string]string{"response_type": "token", "code_challenge_method": "plain", "resource": "https://elsewhere.example/mcp"} {
		p := url.Values{}
		for kk, vv := range base {
			p[kk] = vv
		}
		p.Set(k, v)
		res, err := e.get(t, e.ts.URL+"/authorize?"+p.Encode())
		if err != nil {
			t.Fatal(err)
		}
		loc, _ := url.Parse(res.Header.Get("Location"))
		if res.StatusCode != http.StatusFound || loc.Query().Get("error") == "" {
			t.Errorf("%s=%s: %d %s", k, v, res.StatusCode, loc)
		}
	}
	p := url.Values{"client_id": {id}, "redirect_uri": {"https://claude.ai/other"}}
	res, _ := e.get(t, e.ts.URL+"/authorize?"+p.Encode())
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("unregistered redirect = %d", res.StatusCode)
	}

	res, _ = e.get(t, e.ts.URL+"/callback?state=forged&code=google-code")
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("callback without cookie = %d", res.StatusCode)
	}

	e.google.next.EmailVerified = false
	q := e.authorize(t, base)
	if q.Get("error") != "access_denied" {
		t.Errorf("unverified email = %v", q)
	}
	e.google.fail = true
	res, err := e.get(t, e.ts.URL+"/authorize?"+base.Encode())
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusInternalServerError {
		t.Errorf("upstream failure = %d", res.StatusCode)
	}

	if st, _ := e.post(t, "/token", url.Values{"grant_type": {"authorization_code"}, "client_id": {"nobody"}}, "", ""); st != http.StatusUnauthorized {
		t.Errorf("unknown client = %d", st)
	}
	if !pkceOK(strings.Repeat("a", 43), challenge(strings.Repeat("a", 43))) || pkceOK("short", challenge("short")) {
		t.Error("pkceOK")
	}
}

// reply is a response whose body was already read and closed.
type reply struct {
	StatusCode int
	Header     http.Header
	Body       *bytes.Reader
}

// do sends req and returns the response with its body already read.
func (e *env) do(t *testing.T, req *http.Request) *reply {
	t.Helper()
	res, err := e.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(res.Body)
	if cerr := res.Body.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatal(err)
	}
	return &reply{StatusCode: res.StatusCode, Header: res.Header, Body: bytes.NewReader(body)}
}

func (e *env) get(t *testing.T, u string) (*reply, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	return e.do(t, req), nil
}
