package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.icco.me/foods/internal/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }

func setup(t *testing.T) (*Store, *User) {
	t.Helper()
	s := New(testdb.Open(t))
	ctx := context.Background()
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	u, err := s.FindOrCreateUser(ctx, "google", uuid.NewString(), "alice@example.com")
	if err != nil {
		t.Fatal(err)
	}
	return s, u
}

func ptr[T any](v T) *T { return &v }

func TestUsers(t *testing.T) {
	s, u := setup(t)
	ctx := context.Background()
	again, err := s.FindOrCreateUser(ctx, "google", u.Subject, "ada@example.com")
	if err != nil || again.ID != u.ID || again.Email != "ada@example.com" {
		t.Fatalf("again = %+v, %v", again, err)
	}
	if u.TimeZone != DefaultTimeZone {
		t.Errorf("tz = %q", u.TimeZone)
	}
	if err := s.SetTimeZone(ctx, u.ID, "Europe/London"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetUser(ctx, u.ID)
	if err != nil || got.TimeZone != "Europe/London" || got.Email != "ada@example.com" {
		t.Fatalf("GetUser = %+v, %v", got, err)
	}
	if _, err := s.GetUser(ctx, uuid.NewString()); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing user: %v", err)
	}
	w, err := s.FindWaterFood(ctx, u.ID)
	if err != nil || w == nil || !w.Verified {
		t.Fatalf("water = %+v, %v", w, err)
	}
}

func TestFoods(t *testing.T) {
	s, u := setup(t)
	_, bob := setup(t)
	ctx := context.Background()

	f := &Food{Name: "Cottage cheese 2%", Brand: ptr("Good Culture"), Kcal: 90, ProteinG: 12, Source: SourceUSDA, ExternalID: ptr("111")}
	if err := s.CreateFood(ctx, u.ID, f); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateFood(ctx, u.ID, &Food{Name: "dup", Source: SourceUSDA, ExternalID: ptr("111")}); !errors.Is(err, ErrDuplicate) {
		t.Errorf("duplicate external id: %v", err)
	}
	if err := s.CreateFood(ctx, bob.ID, &Food{Name: "bob's", Source: SourceUSDA, ExternalID: ptr("111")}); err != nil {
		t.Errorf("another user may import the same food: %v", err)
	}

	for _, q := range []string{"cottage", "good cheese", "CULTURE 2"} {
		got, err := s.SearchFoods(ctx, u.ID, q, 5)
		if err != nil || len(got) != 1 || got[0].ID != f.ID {
			t.Errorf("search %q = %v, %v", q, got, err)
		}
	}
	if got, _ := s.SearchFoods(ctx, bob.ID, "cottage", 5); len(got) != 0 {
		t.Errorf("bob sees alice's food: %v", got)
	}
	if got, _ := s.SearchFoods(ctx, u.ID, "%%", 5); len(got) != 0 {
		t.Errorf("punctuation only: %v", got)
	}

	if _, err := s.GetFood(ctx, bob.ID, f.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("bob reads alice's food: %v", err)
	}
	if _, err := s.GetFood(ctx, u.ID, "not-a-uuid"); !errors.Is(err, ErrNotFound) {
		t.Errorf("bad id: %v", err)
	}
	m, err := s.GetFoods(ctx, u.ID, []string{f.ID, f.ID, "junk", uuid.NewString()})
	if err != nil || len(m) != 1 || m[f.ID] == nil {
		t.Fatalf("GetFoods = %v, %v", m, err)
	}
	if m, _ := s.GetFoods(ctx, u.ID, nil); len(m) != 0 {
		t.Error("empty ids")
	}
	ext, err := s.GetFoodByExternal(ctx, u.ID, SourceUSDA, "111")
	if err != nil || ext == nil || ext.ID != f.ID {
		t.Fatalf("by external = %+v, %v", ext, err)
	}
	if none, err := s.GetFoodByExternal(ctx, u.ID, SourceOFF, "111"); err != nil || none != nil {
		t.Fatalf("other source = %+v, %v", none, err)
	}

	var upd Food
	if err := s.UpdateRow(ctx, u.ID, f.ID, &upd, map[string]any{"name": "Cottage cheese", "brand": nil}); err != nil {
		t.Fatal(err)
	}
	if upd.Name != "Cottage cheese" || upd.Brand != nil {
		t.Errorf("updated = %+v", upd)
	}
	if err := s.UpdateRow(ctx, bob.ID, f.ID, &Food{}, map[string]any{"name": "x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("bob updates alice's food: %v", err)
	}
	if err := s.UpdateRow(ctx, u.ID, "junk", &Food{}, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("junk id: %v", err)
	}
	if err := s.UpdateRow(ctx, u.ID, uuid.NewString(), &Food{}, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing id: %v", err)
	}
	if ok, err := s.Archive(ctx, u.ID, f.ID, &Food{}); !ok || err != nil {
		t.Fatalf("archive = %v, %v", ok, err)
	}
	if got, _ := s.SearchFoods(ctx, u.ID, "cottage", 5); len(got) != 0 {
		t.Errorf("archived food still found: %v", got)
	}
	if ok, _ := s.Archive(ctx, u.ID, "junk", &Food{}); ok {
		t.Error("archived junk id")
	}
}

func TestLogsAndRequests(t *testing.T) {
	s, u := setup(t)
	_, bob := setup(t)
	ctx := context.Background()
	water, _ := s.FindWaterFood(ctx, u.ID)
	bobWater, _ := s.FindWaterFood(ctx, bob.ID)
	at := time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC)

	ids, err := s.CreateFoodLogs(ctx, u.ID, "req-00001", "2026-03-14", []*FoodLog{{Date: "2026-03-14", MealType: "water", FoodID: water.ID, Grams: 500, Eaten: true, EatenAt: at}})
	if err != nil || len(ids) != 1 {
		t.Fatalf("create = %v, %v", ids, err)
	}
	if _, err := s.CreateFoodLogs(ctx, u.ID, "req-00001", "2026-03-14", []*FoodLog{{Date: "2026-03-14", MealType: "water", FoodID: water.ID, Grams: 1, EatenAt: at}}); !errors.Is(err, ErrDuplicate) {
		t.Errorf("repeat request: %v", err)
	}
	if _, err := s.CreateFoodLogs(ctx, u.ID, "req-00002", "2026-03-14", []*FoodLog{
		{Date: "2026-03-14", MealType: "water", FoodID: water.ID, Grams: 1, EatenAt: at},
		{Date: "2026-03-14", MealType: "water", FoodID: bobWater.ID, Grams: 1, EatenAt: at},
	}); !errors.Is(err, ErrNotFound) {
		t.Errorf("another user's food: %v", err)
	}
	if r, _ := s.FindRequest(ctx, u.ID, RequestFood, "req-00002"); r != nil {
		t.Error("failed request was recorded")
	}
	r, err := s.FindRequest(ctx, u.ID, RequestFood, "req-00001")
	if err != nil || r == nil || r.IDs[0] != ids[0] || r.Date != "2026-03-14" {
		t.Fatalf("request = %+v, %v", r, err)
	}

	day, err := s.DayFoodLogs(ctx, u.ID, "2026-03-14")
	if err != nil || len(day) != 1 || day[0].Food.Name != WaterFoodName {
		t.Fatalf("day = %+v, %v", day, err)
	}
	l, err := s.GetFoodLog(ctx, u.ID, ids[0])
	if err != nil || l.Grams != 500 {
		t.Fatalf("log = %+v, %v", l, err)
	}
	if _, err := s.GetFoodLog(ctx, bob.ID, ids[0]); !errors.Is(err, ErrNotFound) {
		t.Errorf("bob reads alice's log: %v", err)
	}
	if _, err := s.GetFoodLog(ctx, u.ID, "junk"); !errors.Is(err, ErrNotFound) {
		t.Errorf("junk: %v", err)
	}
	if ok, _ := s.Archive(ctx, bob.ID, ids[0], &FoodLog{}); ok {
		t.Error("bob archived alice's log")
	}
	if ok, _ := s.Archive(ctx, u.ID, ids[0], &FoodLog{}); !ok {
		t.Error("archive failed")
	}
	if ok, _ := s.Archive(ctx, u.ID, ids[0], &FoodLog{}); ok {
		t.Error("archived twice")
	}
	if day, _ := s.DayFoodLogs(ctx, u.ID, "2026-03-14"); len(day) != 0 {
		t.Errorf("archived log listed: %v", day)
	}
}

func TestTargetsAndWorkouts(t *testing.T) {
	s, u := setup(t)
	ctx := context.Background()
	if got, err := s.GetTargets(ctx, u.ID); err != nil || got != nil {
		t.Fatalf("no targets = %+v, %v", got, err)
	}
	if (*Target)(nil).Nutrition().Kcal != nil {
		t.Error("nil targets")
	}
	if err := s.SaveTargets(ctx, u.ID, &Target{Kcal: ptr(2000)}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveTargets(ctx, u.ID, &Target{Kcal: ptr(1800), WaterMl: ptr(2000)}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetTargets(ctx, u.ID)
	if err != nil || *got.Kcal != 1800 || *got.Nutrition().WaterMl != 2000 {
		t.Fatalf("targets = %+v, %v", got, err)
	}

	w := &Workout{Date: "2026-03-14", Type: "run", DurationMinutes: 30}
	if err := s.CreateWorkout(ctx, u.ID, "req-w0001", w); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateWorkout(ctx, u.ID, "req-w0001", &Workout{Date: "2026-03-14", Type: "run", DurationMinutes: 1}); !errors.Is(err, ErrDuplicate) {
		t.Errorf("repeat: %v", err)
	}
	ws, err := s.DayWorkouts(ctx, u.ID, "2026-03-14")
	if err != nil || len(ws) != 1 {
		t.Fatalf("day workouts = %v, %v", ws, err)
	}
	if g, err := s.GetWorkout(ctx, u.ID, w.ID); err != nil || g.DurationMinutes != 30 {
		t.Fatalf("get = %+v, %v", g, err)
	}
	if _, err := s.GetWorkout(ctx, u.ID, "junk"); !errors.Is(err, ErrNotFound) {
		t.Error("junk")
	}
	if ok, _ := s.Archive(ctx, u.ID, w.ID, &Workout{}); !ok {
		t.Error("archive")
	}
	if _, err := s.GetWorkout(ctx, u.ID, w.ID); !errors.Is(err, ErrNotFound) {
		t.Error("archived workout still readable")
	}
}
