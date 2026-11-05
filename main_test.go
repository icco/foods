package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.icco.me/foods/internal/foodapi"
	"go.icco.me/foods/internal/oauth"
	"go.icco.me/foods/internal/testdb"
	"go.uber.org/zap"
)

func TestMain(m *testing.M) { testdb.Main(m) }

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func full() map[string]string {
	return map[string]string{
		"DATABASE_URL": "postgres://alice@db/foods", "BASE_URL": "https://foods.example.com/", "GOOGLE_CLIENT_ID": "id", "GOOGLE_CLIENT_SECRET": "secret",
		"ALLOWED_EMAILS": "alice@example.com, bob@example.com", "USDA_API_KEY": "k", "REDIRECT_HOSTS": "chat.example.com",
	}
}

func TestLoadConfig(t *testing.T) {
	c, err := loadConfig(env(full()))
	if err != nil {
		t.Fatal(err)
	}
	if c.BaseURL != "https://foods.example.com" || c.Port != "8080" || len(c.AllowedEmails) != 2 || c.RedirectHosts[len(c.RedirectHosts)-1] != "chat.example.com" {
		t.Errorf("config = %+v", c)
	}
	m := full()
	delete(m, "USDA_API_KEY")
	m["ALLOWED_EMAILS"] = " , "
	if _, err := loadConfig(env(m)); err == nil || !strings.Contains(err.Error(), "USDA_API_KEY") || !strings.Contains(err.Error(), "ALLOWED_EMAILS") {
		t.Errorf("missing = %v", err)
	}
	m = full()
	m["BASE_URL"] = "http://foods.example.com"
	if _, err := loadConfig(env(m)); err == nil {
		t.Error("plain http base url accepted")
	}
	if err := run(context.Background(), env(map[string]string{}), zap.NewNop().Sugar()); err == nil {
		t.Error("run without config")
	}
}

type noUpstream struct{}

func (noUpstream) AuthCodeURL(string, string) string { return "" }
func (noUpstream) Exchange(context.Context, string, string) (*oauth.Identity, error) {
	return nil, io.EOF
}

func TestHandler(t *testing.T) {
	c, _ := loadConfig(env(full()))
	h, err := newHandler(context.Background(), c, testdb.Open(t), noUpstream{}, foodapi.New("k"), zap.NewNop().Sugar())
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(h)
	defer ts.Close()

	for path, want := range map[string]string{"/": "/mcp as a custom connector", "/healthz": "ok", "/.well-known/oauth-authorization-server": `"registration_endpoint":"https://foods.example.com/register"`} {
		code, _, body := send(t, http.MethodGet, ts.URL+path, "")
		if code != http.StatusOK || !strings.Contains(string(body), want) {
			t.Errorf("%s = %d %s", path, code, body)
		}
	}

	code, hdr, _ := send(t, http.MethodPost, ts.URL+"/mcp", `{}`)
	if code != http.StatusUnauthorized || !strings.Contains(hdr.Get("WWW-Authenticate"), `resource_metadata="https://foods.example.com/.well-known/oauth-protected-resource/mcp"`) {
		t.Errorf("unauthenticated /mcp = %d %q", code, hdr.Get("WWW-Authenticate"))
	}
}

func send(t *testing.T, method, u, body string) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, u, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, res.Header, b
}
