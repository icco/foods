package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.icco.me/foods/internal/foodapi"
	"go.icco.me/foods/internal/nutrition"
	"go.icco.me/foods/internal/service"
	"go.icco.me/foods/internal/store"
	"go.icco.me/foods/internal/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }

type fakeDBs struct{}

func (fakeDBs) Search(_ context.Context, s foodapi.Source, _ string, _ int) ([]foodapi.Food, error) {
	if s == foodapi.OFF {
		return nil, foodapi.ErrUnavailable
	}
	brand := "Quaker"
	return []foodapi.Food{{Source: s, ExternalID: "173904", Name: "Oats", Brand: &brand, Per100: nutrition.Values{Kcal: 379, ProteinG: 13.2, FatG: 6.5, CarbsG: 67.7}}}, nil
}

func (fakeDBs) Get(ctx context.Context, s foodapi.Source, _ string) (*foodapi.Food, error) {
	f, err := fakeDBs{}.Search(ctx, s, "", 1)
	if err != nil {
		return nil, err
	}
	return &f[0], nil
}

type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func connect(t *testing.T) (*mcp.ClientSession, *service.Service, string) {
	t.Helper()
	ctx := context.Background()
	st := store.New(testdb.Open(t))
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	u, err := st.FindOrCreateUser(ctx, "google", uuid.NewString(), "alice@example.com")
	if err != nil {
		t.Fatal(err)
	}
	svc := service.New(st, fakeDBs{})
	svc.Now = func() time.Time { return time.Date(2026, 3, 14, 16, 0, 0, 0, time.UTC) }
	srv := mcp.NewServer(&mcp.Implementation{Name: "foods", Version: "test"}, &mcp.ServerOptions{Instructions: Instructions})
	Register(srv, svc)
	verify := func(_ context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
		if token != "good" {
			return nil, auth.ErrInvalidToken
		}
		return &auth.TokenInfo{UserID: u.ID, Expiration: time.Now().Add(time.Hour)}, nil
	}
	h := auth.RequireBearerToken(verify, nil)(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil))
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)

	client := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil)
	cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL, HTTPClient: &http.Client{Transport: bearer{"good"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cs.Close(); err != nil {
			t.Error(err)
		}
	})
	return cs, svc, u.ID
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String(), res.IsError
}

func must(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any, wants ...string) string {
	t.Helper()
	out, isErr := call(t, cs, name, args)
	if isErr {
		t.Fatalf("%s returned an error: %s", name, out)
	}
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("%s = %q, want it to contain %q", name, out, w)
		}
	}
	return out
}

var foodID = regexp.MustCompile(`food_id=([0-9a-f-]{36})`)

func TestToolsEndToEnd(t *testing.T) {
	cs, _, _ := connect(t)
	ctx := context.Background()

	list, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Tools) != 12 {
		t.Errorf("got %d tools", len(list.Tools))
	}
	for _, tool := range list.Tools {
		if tool.Annotations == nil || tool.Annotations.DestructiveHint != nil && *tool.Annotations.DestructiveHint {
			t.Errorf("%s: nothing is destructive: %+v", tool.Name, tool.Annotations)
		}
	}

	out := must(t, cs, "search_foods", map[string]any{"query": "oats"}, "1 match:", "Oats (Quaker)", "external_source=usda external_id=173904 [not saved", "379 kcal", "Open Food Facts is unavailable")
	_ = out
	out = must(t, cs, "import_food", map[string]any{"external_source": "usda", "external_id": "173904"}, "Saved: Oats")
	oats := foodID.FindStringSubmatch(out)[1]
	must(t, cs, "import_food", map[string]any{"external_source": "usda", "external_id": "173904"}, "Already saved")
	must(t, cs, "search_foods", map[string]any{"query": "oats", "include_external": false}, "food_id="+oats+" [saved]")
	must(t, cs, "search_foods", map[string]any{"query": "zzz", "include_external": false}, "No matches.")

	out = must(t, cs, "add_food", map[string]any{"name": "Blueberries", "per_100g": map[string]any{"kcal": 57, "protein_g": 0.7, "fat_g": 0.3, "carbs_g": 14.5}}, "Saved Blueberries: 57 kcal, 0.7 g protein")
	berries := foodID.FindStringSubmatch(out)[1]
	must(t, cs, "update_food", map[string]any{"food_id": berries, "serving_g": 148, "archived": true}, "serving 148 g", "[archived]")

	must(t, cs, "update_settings", map[string]any{"time_zone": "America/Chicago", "targets": map[string]any{"kcal": 2000, "protein_g": 120}}, "Time zone America/Chicago", "kcal=2000", "protein_g=120")
	must(t, cs, "update_settings", map[string]any{"clear_targets": []string{"kcal", "protein_g"}}, "Targets: none set.")
	must(t, cs, "update_settings", map[string]any{"targets": map[string]any{"kcal": 2000, "protein_g": 120}}, "kcal=2000")

	out = must(t, cs, "log_entries", map[string]any{
		"client_request_id": "c9b1e7d0-breakfast", "meal_type": "breakfast",
		"entries": []any{map[string]any{"food_id": oats, "grams": 40}, map[string]any{"food_id": berries, "servings": 0.5}}, "water_ml": 250,
	}, "Logged 3 entries.", "Day total so far: 194 kcal", "1806 kcal left.")
	must(t, cs, "log_entries", map[string]any{"client_request_id": "c9b1e7d0-breakfast", "water_ml": 1}, "Already logged")
	logID := regexp.MustCompile(`log_ids=\[([0-9a-f-]{36})`).FindStringSubmatch(out)[1]

	must(t, cs, "log_entries", map[string]any{"client_request_id": "c9b1e7d0-dinner", "meal_type": "dinner", "eaten": false, "entries": []any{map[string]any{"food_id": oats, "grams": 100}}}, "Logged 1 entry.")
	must(t, cs, "get_day", map[string]any{}, "4 food entries and 0 workouts on 2026-03-14.", "- breakfast: 40 g Oats (eaten) 152 kcal", "(planned)", "250 ml water", "Including planned:", "Left today (negative is over): 1806 kcal")
	must(t, cs, "update_log", map[string]any{"log_id": logID, "grams": 80}, "Updated: 80 g Oats 303 kcal")
	must(t, cs, "archive_log", map[string]any{"log_id": logID}, "Archived 80 g Oats (breakfast, 2026-03-14)")
	must(t, cs, "archive_log", map[string]any{"log_id": logID}, "already archived")

	out = must(t, cs, "log_workout", map[string]any{"client_request_id": "c9b1e7d0-run", "type": "run", "duration_minutes": 32.5, "note": "easy"}, "Logged 32.5 min of run on 2026-03-14.")
	wid := regexp.MustCompile(`workout_id=(\S+)`).FindStringSubmatch(out)[1]
	must(t, cs, "log_workout", map[string]any{"client_request_id": "c9b1e7d0-run", "type": "run", "duration_minutes": 1}, "Already logged")
	must(t, cs, "get_day", map[string]any{"date": "2026-03-14"}, "1 workout on", "- workout: 32.5 min run (easy)")
	must(t, cs, "update_workout", map[string]any{"workout_id": wid, "type": "walk"}, "Updated: 32.5 min of walk")
	must(t, cs, "archive_workout", map[string]any{"workout_id": wid}, "Archived the 32.5 min walk workout")
	must(t, cs, "archive_workout", map[string]any{"workout_id": wid}, "already archived")

	if out, isErr := call(t, cs, "log_entries", map[string]any{"client_request_id": "c9b1e7d0-bad", "water_ml": 9000}); !isErr || !strings.Contains(out, "water_ml") {
		t.Errorf("user error = %q, %v", out, isErr)
	}
	if out, isErr := call(t, cs, "log_entries", map[string]any{"client_request_id": "c9b1e7d0-bad", "meal_type": "brunch"}); !isErr || !strings.Contains(out, "meal_type") {
		t.Errorf("enum = %q, %v", out, isErr)
	}
}

func TestUserID(t *testing.T) {
	if _, err := UserID(nil); err == nil {
		t.Error("nil request")
	}
	if _, err := UserID(&mcp.CallToolRequest{Extra: &mcp.RequestExtra{}}); err == nil {
		t.Error("no token")
	}
	if num(2.25) != "2.3" || num(3) != "3" || num(-0.04) != "0" {
		t.Errorf("num: %s %s %s", num(2.25), num(3), num(-0.04))
	}
}
