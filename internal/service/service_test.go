package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.icco.me/foods/internal/foodapi"
	"go.icco.me/foods/internal/nutrition"
	"go.icco.me/foods/internal/store"
	"go.icco.me/foods/internal/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }

func ptr[T any](v T) *T { return &v }

type fakeDBs struct {
	foods map[foodapi.Source][]foodapi.Food
	down  map[foodapi.Source]bool
}

func (f *fakeDBs) Search(_ context.Context, s foodapi.Source, _ string, limit int) ([]foodapi.Food, error) {
	if f.down[s] {
		return nil, fmt.Errorf("%w: down", foodapi.ErrUnavailable)
	}
	return f.foods[s][:min(limit, len(f.foods[s]))], nil
}

func (f *fakeDBs) Get(_ context.Context, s foodapi.Source, id string) (*foodapi.Food, error) {
	if f.down[s] {
		return nil, fmt.Errorf("%w: down", foodapi.ErrUnavailable)
	}
	for _, x := range f.foods[s] {
		if x.ExternalID == id {
			return &x, nil
		}
	}
	return nil, nil
}

func food(src foodapi.Source, id, name string, kcal float64) foodapi.Food {
	return foodapi.Food{Source: src, ExternalID: id, Name: name, ServingG: ptr(30.0), Per100: nutrition.Values{Kcal: kcal, ProteinG: 10, FatG: 5, CarbsG: 20}}
}

// noon is 2026-03-14 12:00 in New York.
var noon = time.Date(2026, 3, 14, 16, 0, 0, 0, time.UTC)

func setup(t *testing.T) (*Service, string, *fakeDBs) {
	t.Helper()
	st := store.New(testdb.Open(t))
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	u, err := st.FindOrCreateUser(ctx, "google", uuid.NewString(), "alice@example.com")
	if err != nil {
		t.Fatal(err)
	}
	dbs := &fakeDBs{foods: map[foodapi.Source][]foodapi.Food{
		foodapi.USDA: {food(foodapi.USDA, "1", "Oats", 380), food(foodapi.USDA, "2", "Bad oats", 5330), food(foodapi.USDA, "3", "Oat milk", 50)},
		foodapi.OFF:  {food(foodapi.OFF, "a", "Oat bar", 400)},
	}, down: map[foodapi.Source]bool{}}
	s := New(st, dbs)
	s.Now = func() time.Time { return noon }
	return s, u.ID, dbs
}

func wantUserErr(t *testing.T, err error, contains string) {
	t.Helper()
	if !IsUserError(err) || !strings.Contains(err.Error(), contains) {
		t.Errorf("err = %v, want user error containing %q", err, contains)
	}
}

func TestSearchAndImport(t *testing.T) {
	s, uid, dbs := setup(t)
	ctx := context.Background()

	r, err := s.SearchFoods(ctx, uid, SearchInput{Query: "oat", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range r.Results {
		names = append(names, f.Name)
	}
	if strings.Join(names, ",") != "Oats,Oat bar,Oat milk" {
		t.Errorf("results = %v", names)
	}
	if len(r.Warnings) != 1 || !strings.Contains(r.Warnings[0], "Hid 1") {
		t.Errorf("warnings = %v", r.Warnings)
	}

	imp, err := s.ImportFood(ctx, uid, ImportFoodInput{ExternalSource: foodapi.USDA, ExternalID: "1"})
	if err != nil || imp.AlreadySaved || imp.Per100.Kcal != 380 {
		t.Fatalf("import = %+v, %v", imp, err)
	}
	again, err := s.ImportFood(ctx, uid, ImportFoodInput{ExternalSource: foodapi.USDA, ExternalID: "1"})
	if err != nil || !again.AlreadySaved || again.FoodID != imp.FoodID {
		t.Fatalf("again = %+v, %v", again, err)
	}

	r, _ = s.SearchFoods(ctx, uid, SearchInput{Query: "oats", Limit: 10})
	if r.Results[0].FoodID != imp.FoodID {
		t.Errorf("saved food should come first: %+v", r.Results)
	}
	for _, f := range r.Results[1:] {
		if f.ExternalID == "1" {
			t.Error("saved food listed twice")
		}
	}
	r, _ = s.SearchFoods(ctx, uid, SearchInput{Query: "oats", IncludeExternal: ptr(false)})
	if len(r.Results) != 1 {
		t.Errorf("local only = %+v", r.Results)
	}

	dbs.down[foodapi.OFF] = true
	r, err = s.SearchFoods(ctx, uid, SearchInput{Query: "milk"})
	if err != nil || len(r.Warnings) == 0 || !strings.Contains(r.Warnings[0], "Open Food Facts") {
		t.Errorf("one down = %+v, %v", r, err)
	}
	dbs.down[foodapi.USDA] = true
	_, err = s.SearchFoods(ctx, uid, SearchInput{Query: "milk"})
	wantUserErr(t, err, "Could not reach")
	_, err = s.ImportFood(ctx, uid, ImportFoodInput{ExternalSource: foodapi.USDA, ExternalID: "3"})
	wantUserErr(t, err, "unavailable")
	dbs.down = map[foodapi.Source]bool{}

	_, err = s.ImportFood(ctx, uid, ImportFoodInput{ExternalSource: foodapi.USDA, ExternalID: "2"})
	wantUserErr(t, err, "impossible")
	_, err = s.ImportFood(ctx, uid, ImportFoodInput{ExternalSource: foodapi.USDA, ExternalID: "404"})
	wantUserErr(t, err, "No food")
	_, err = s.ImportFood(ctx, uid, ImportFoodInput{ExternalSource: "fatsecret", ExternalID: "1"})
	wantUserErr(t, err, "usda or off")
	_, err = s.ImportFood(ctx, uid, ImportFoodInput{ExternalSource: foodapi.OFF})
	wantUserErr(t, err, "external_id")
	_, err = s.SearchFoods(ctx, uid, SearchInput{Query: " "})
	wantUserErr(t, err, "query")
	_, err = s.SearchFoods(ctx, uid, SearchInput{Query: "x", Limit: 21})
	wantUserErr(t, err, "limit")
}

func TestAddAndUpdateFood(t *testing.T) {
	s, uid, _ := setup(t)
	ctx := context.Background()

	label, err := s.AddFood(ctx, uid, AddFoodInput{Name: " Yogurt ", ServingG: ptr(150.0), PerServing: &NutrientsInput{Kcal: 150, ProteinG: 15, FatG: 3, CarbsG: 12, SodiumMg: ptr(75.0)}})
	if err != nil {
		t.Fatal(err)
	}
	if label.Name != "Yogurt" || label.Per100.Kcal != 100 || label.Per100.ProteinG != 10 || label.Per100.SodiumMg != 50 {
		t.Errorf("label = %+v", label)
	}
	granola, err := s.AddFood(ctx, uid, AddFoodInput{Name: "Granola", Per100G: &NutrientsInput{Kcal: 450, ProteinG: 10, FatG: 15, CarbsG: 65}})
	if err != nil {
		t.Fatal(err)
	}
	meal, err := s.AddFood(ctx, uid, AddFoodInput{Name: "Parfait", Items: []RecipeItem{{FoodID: label.FoodID, Grams: 150}, {FoodID: granola.FoodID, Grams: 50}}})
	if err != nil {
		t.Fatal(err)
	}
	// 150 + 225 kcal over 200 g.
	if *meal.ServingG != 200 || meal.Per100.Kcal != 188 {
		t.Errorf("meal = %+v", meal)
	}
	est, err := s.AddFood(ctx, uid, AddFoodInput{Name: "Diner burger", Totals: &TotalsInput{Kcal: 600, ProteinG: ptr(30.0)}})
	if err != nil || *est.ServingG != 100 || est.Per100.Kcal != 600 {
		t.Errorf("estimate = %+v, %v", est, err)
	}
	est, err = s.AddFood(ctx, uid, AddFoodInput{Name: "Pasta", Totals: &TotalsInput{Kcal: 600, TotalGrams: ptr(300.0)}})
	if err != nil || est.Per100.Kcal != 200 {
		t.Errorf("estimate with weight = %+v, %v", est, err)
	}

	bad := []struct {
		in   AddFoodInput
		want string
	}{
		{AddFoodInput{Name: ""}, "name"},
		{AddFoodInput{Name: "x"}, "exactly one"},
		{AddFoodInput{Name: "x", Per100G: &NutrientsInput{Kcal: 1}, Totals: &TotalsInput{}}, "exactly one"},
		{AddFoodInput{Name: "x", Source: "usda", Per100G: &NutrientsInput{}}, "import_food"},
		{AddFoodInput{Name: "x", Per100G: &NutrientsInput{Kcal: 1500}}, "1000 kcal"},
		{AddFoodInput{Name: "x", Per100G: &NutrientsInput{ProteinG: 101}}, "above 100"},
		{AddFoodInput{Name: "x", Per100G: &NutrientsInput{ProteinG: 50, FatG: 50, CarbsG: 50}}, "together"},
		{AddFoodInput{Name: "x", Per100G: &NutrientsInput{SodiumMg: ptr(50000.0)}}, "Sodium"},
		{AddFoodInput{Name: "x", Per100G: &NutrientsInput{Kcal: -1}}, "negative"},
		{AddFoodInput{Name: "x", PerServing: &NutrientsInput{}}, "serving_g"},
		{AddFoodInput{Name: "x", ServingG: ptr(-1.0), PerServing: &NutrientsInput{}}, "serving_g"},
		{AddFoodInput{Name: "x", Items: []RecipeItem{{FoodID: uuid.NewString(), Grams: 1}}}, "No food"},
		{AddFoodInput{Name: "x", Items: []RecipeItem{{FoodID: label.FoodID, Grams: 0}}}, "grams"},
		{AddFoodInput{Name: "x", Items: make([]RecipeItem, 31)}, "30"},
		{AddFoodInput{Name: "x", Totals: &TotalsInput{Kcal: -1}}, "negative"},
		{AddFoodInput{Name: "x", Totals: &TotalsInput{TotalGrams: ptr(0.0)}}, "total_grams"},
	}
	for _, b := range bad {
		_, err := s.AddFood(ctx, uid, b.in)
		wantUserErr(t, err, b.want)
	}

	upd, err := s.UpdateFood(ctx, uid, UpdateFoodInput{FoodID: label.FoodID, Name: ptr("Greek yogurt"), Brand: ptr("Fage"), Notes: ptr("plain"), ServingG: ptr(170.0), Per100G: &NutrientsInput{Kcal: 97, ProteinG: 9, FatG: 5, CarbsG: 4}})
	if err != nil || upd.Name != "Greek yogurt" || upd.Per100.Kcal != 97 || *upd.ServingG != 170 {
		t.Fatalf("update = %+v, %v", upd, err)
	}
	upd, err = s.UpdateFood(ctx, uid, UpdateFoodInput{FoodID: label.FoodID, Clear: []FoodField{"brand", "serving_g", "notes"}, Archived: ptr(true)})
	if err != nil || upd.ServingG != nil || !upd.Archived {
		t.Fatalf("clear = %+v, %v", upd, err)
	}
	upd, err = s.UpdateFood(ctx, uid, UpdateFoodInput{FoodID: label.FoodID, Archived: ptr(false)})
	if err != nil || upd.Archived {
		t.Fatalf("unarchive = %+v, %v", upd, err)
	}
	_, err = s.UpdateFood(ctx, uid, UpdateFoodInput{FoodID: uuid.NewString(), Name: ptr("x")})
	wantUserErr(t, err, "No food")
	_, err = s.UpdateFood(ctx, uid, UpdateFoodInput{FoodID: label.FoodID, Clear: []FoodField{"kcal"}})
	wantUserErr(t, err, "clear")
	_, err = s.UpdateFood(ctx, uid, UpdateFoodInput{FoodID: label.FoodID, Name: ptr(" ")})
	wantUserErr(t, err, "name")
	_, err = s.UpdateFood(ctx, uid, UpdateFoodInput{FoodID: label.FoodID, ServingG: ptr(0.0)})
	wantUserErr(t, err, "serving_g")
	_, err = s.UpdateFood(ctx, uid, UpdateFoodInput{FoodID: label.FoodID, Per100G: &NutrientsInput{Kcal: 2000}})
	wantUserErr(t, err, "1000")
}

func TestLogging(t *testing.T) {
	s, uid, _ := setup(t)
	ctx := context.Background()
	oats, err := s.AddFood(ctx, uid, AddFoodInput{Name: "Oats", ServingG: ptr(40.0), Per100G: &NutrientsInput{Kcal: 380, ProteinG: 13, FatG: 7, CarbsG: 67}})
	if err != nil {
		t.Fatal(err)
	}
	nolabel, _ := s.AddFood(ctx, uid, AddFoodInput{Name: "Blueberries", Per100G: &NutrientsInput{Kcal: 57, ProteinG: 0.7, FatG: 0.3, CarbsG: 14}})
	if _, err := s.UpdateSettings(ctx, uid, UpdateSettingsInput{Targets: &TargetsInput{Kcal: ptr(2000), ProteinG: ptr(100), WaterMl: ptr(2000)}}); err != nil {
		t.Fatal(err)
	}

	in := LogEntriesInput{ClientRequestID: "req-breakfast", MealType: "breakfast", Entries: []EntryInput{{FoodID: oats.FoodID, Servings: ptr(2.0)}, {FoodID: nolabel.FoodID, Grams: ptr(100.0)}}, WaterMl: ptr(500.0), Note: ptr("at home"), MindfulnessScore: ptr(8.0)}
	got, err := s.LogEntries(ctx, uid, in)
	if err != nil {
		t.Fatal(err)
	}
	// 80 g oats = 304 kcal, 100 g blueberries = 57 kcal.
	if len(got.LogIDs) != 3 || got.AlreadyLogged || got.Day.Eaten.Kcal != 361 || got.Day.WaterMl != 500 {
		t.Fatalf("logged = %+v", got)
	}
	if left, _ := got.Day.RemainingValue("kcal"); left != 1639 {
		t.Errorf("kcal left = %v", left)
	}
	again, err := s.LogEntries(ctx, uid, in)
	if err != nil || !again.AlreadyLogged || again.LogIDs[0] != got.LogIDs[0] {
		t.Fatalf("again = %+v, %v", again, err)
	}

	planned, err := s.LogEntries(ctx, uid, LogEntriesInput{ClientRequestID: "req-dinner-1", MealType: "dinner", Entries: []EntryInput{{FoodID: oats.FoodID, Grams: ptr(100.0)}}, Eaten: ptr(false), Date: "2026-03-14"})
	if err != nil || planned.Day.Eaten.Kcal != 361 || planned.Day.Planned.Kcal != 741 {
		t.Fatalf("planned = %+v, %v", planned, err)
	}
	late, err := s.LogEntries(ctx, uid, LogEntriesInput{ClientRequestID: "req-late-01", MealType: "snack", Entries: []EntryInput{{FoodID: oats.FoodID, Grams: ptr(10.0)}}, EatenAt: "2026-03-15T03:30:00Z"})
	if err != nil || late.Day.Date != "2026-03-14" {
		t.Fatalf("late night in New York belongs to the 14th: %+v, %v", late, err)
	}

	day, err := s.GetDay(ctx, uid, "")
	if err != nil || day.Date != "2026-03-14" || len(day.FoodLogs) != 5 {
		t.Fatalf("day = %+v, %v", day, err)
	}
	first := day.FoodLogs[0]
	if first.MealType != "breakfast" || first.Grams != 80 || *first.MindfulnessScore != 8 || first.Nutrients.Kcal != 304 {
		t.Errorf("first log = %+v", first)
	}

	ch, err := s.UpdateLog(ctx, uid, UpdateLogInput{LogID: planned.LogIDs[0], Eaten: ptr(true), Grams: ptr(50.0), MealType: "lunch", Note: ptr("leftovers"), MindfulnessScore: ptr(2.0)})
	if err != nil || ch.Log.Grams != 50 || ch.Log.MealType != "lunch" || ch.Day.Eaten.Kcal != 361+190+38 {
		t.Fatalf("update = %+v, %v", ch, err)
	}
	ch, err = s.UpdateLog(ctx, uid, UpdateLogInput{LogID: planned.LogIDs[0], Date: "2026-03-13", Clear: []LogField{"note", "mindfulness_score"}})
	if err != nil || ch.Date != "2026-03-13" || ch.Log.Note != nil || ch.Log.MindfulnessScore != nil || !ch.Log.EatenAt.Equal(time.Date(2026, 3, 13, 16, 0, 0, 0, time.UTC)) {
		t.Fatalf("move = %+v, %v", ch, err)
	}

	arch, err := s.ArchiveLog(ctx, uid, got.LogIDs[0])
	if err != nil || !arch.Archived || arch.Log.FoodName != "Oats" || arch.Day.Eaten.Kcal != 57+38 {
		t.Fatalf("archive = %+v, %v", arch, err)
	}
	if arch, err := s.ArchiveLog(ctx, uid, got.LogIDs[0]); err != nil || arch.Archived {
		t.Errorf("archive twice = %+v, %v", arch, err)
	}

	bad := []struct {
		in   LogEntriesInput
		want string
	}{
		{LogEntriesInput{ClientRequestID: "short"}, "client_request_id"},
		{LogEntriesInput{ClientRequestID: "req-bad-01"}, "Send entries"},
		{LogEntriesInput{ClientRequestID: "req-bad-02", Entries: []EntryInput{{FoodID: oats.FoodID, Grams: ptr(1.0)}}}, "meal_type is required"},
		{LogEntriesInput{ClientRequestID: "req-bad-03", MealType: "brunch", WaterMl: ptr(1.0)}, "meal_type must"},
		{LogEntriesInput{ClientRequestID: "req-bad-04", MealType: "water", Entries: []EntryInput{{FoodID: oats.FoodID, Grams: ptr(1.0)}}, WaterMl: ptr(1.0)}, "water_ml for water"},
		{LogEntriesInput{ClientRequestID: "req-bad-05", MealType: "lunch", Entries: []EntryInput{{FoodID: uuid.NewString(), Grams: ptr(1.0)}}}, "No food"},
		{LogEntriesInput{ClientRequestID: "req-bad-06", MealType: "lunch", Entries: []EntryInput{{FoodID: oats.FoodID}}}, "exactly one"},
		{LogEntriesInput{ClientRequestID: "req-bad-07", MealType: "lunch", Entries: []EntryInput{{FoodID: nolabel.FoodID, Servings: ptr(1.0)}}}, "no serving size"},
		{LogEntriesInput{ClientRequestID: "req-bad-08", MealType: "lunch", Entries: []EntryInput{{FoodID: oats.FoodID, Grams: ptr(6000.0)}}}, "5000"},
		{LogEntriesInput{ClientRequestID: "req-bad-09", WaterMl: ptr(-1.0)}, "water_ml"},
		{LogEntriesInput{ClientRequestID: "req-bad-10", WaterMl: ptr(1.0), Date: "2026-02-30"}, "YYYY-MM-DD"},
		{LogEntriesInput{ClientRequestID: "req-bad-11", WaterMl: ptr(1.0), EatenAt: "yesterday"}, "eaten_at"},
		{LogEntriesInput{ClientRequestID: "req-bad-12", WaterMl: ptr(1.0), Note: ptr("")}, "note"},
		{LogEntriesInput{ClientRequestID: "req-bad-13", WaterMl: ptr(1.0), MindfulnessScore: ptr(11.0)}, "mindfulness"},
		{LogEntriesInput{ClientRequestID: "req-bad-14", MealType: "lunch", Entries: make([]EntryInput, 31)}, "30"},
	}
	for _, b := range bad {
		_, err := s.LogEntries(ctx, uid, b.in)
		wantUserErr(t, err, b.want)
	}
	badUpd := []struct {
		in   UpdateLogInput
		want string
	}{
		{UpdateLogInput{LogID: uuid.NewString()}, "No log"},
		{UpdateLogInput{LogID: planned.LogIDs[0], Grams: ptr(0.0)}, "grams"},
		{UpdateLogInput{LogID: planned.LogIDs[0], MealType: "brunch"}, "meal_type"},
		{UpdateLogInput{LogID: planned.LogIDs[0], Note: ptr("")}, "note"},
		{UpdateLogInput{LogID: planned.LogIDs[0], MindfulnessScore: ptr(0.0)}, "mindfulness"},
		{UpdateLogInput{LogID: planned.LogIDs[0], Clear: []LogField{"grams"}}, "clear"},
		{UpdateLogInput{LogID: planned.LogIDs[0], Date: "soon"}, "YYYY-MM-DD"},
	}
	for _, b := range badUpd {
		_, err := s.UpdateLog(ctx, uid, b.in)
		wantUserErr(t, err, b.want)
	}
	_, err = s.GetDay(ctx, uid, "14/03/2026")
	wantUserErr(t, err, "YYYY-MM-DD")

	water, _ := s.Store.FindWaterFood(ctx, uid)
	if _, err := s.UpdateFood(ctx, uid, UpdateFoodInput{FoodID: water.ID, Name: ptr("H2O")}); err != nil {
		t.Fatal(err)
	}
	_, err = s.LogEntries(ctx, uid, LogEntriesInput{ClientRequestID: "req-water-x", WaterMl: ptr(250.0)})
	wantUserErr(t, err, `no food named "Water"`)
}

func TestWorkoutsAndSettings(t *testing.T) {
	s, uid, _ := setup(t)
	ctx := context.Background()

	w, err := s.LogWorkout(ctx, uid, LogWorkoutInput{ClientRequestID: "req-run-001", Type: "run", DurationMinutes: 30, Note: ptr("easy")})
	if err != nil || w.Date != "2026-03-14" || w.AlreadyLogged {
		t.Fatalf("log = %+v, %v", w, err)
	}
	again, err := s.LogWorkout(ctx, uid, LogWorkoutInput{ClientRequestID: "req-run-001", Type: "run", DurationMinutes: 30})
	if err != nil || !again.AlreadyLogged || again.WorkoutID != w.WorkoutID {
		t.Fatalf("again = %+v, %v", again, err)
	}
	upd, err := s.UpdateWorkout(ctx, uid, UpdateWorkoutInput{WorkoutID: w.WorkoutID, Type: "walk", DurationMinutes: ptr(45.0), Date: "2026-03-13", Clear: []WorkoutField{"note"}})
	if err != nil || upd.Type != "walk" || upd.DurationMinutes != 45 || upd.Date != "2026-03-13" || upd.Note != nil {
		t.Fatalf("update = %+v, %v", upd, err)
	}
	upd, err = s.UpdateWorkout(ctx, uid, UpdateWorkoutInput{WorkoutID: w.WorkoutID, Note: ptr("hills")})
	if err != nil || *upd.Note != "hills" {
		t.Fatalf("note = %+v, %v", upd, err)
	}
	day, _ := s.GetDay(ctx, uid, "2026-03-13")
	if len(day.Workouts) != 1 {
		t.Errorf("workouts = %+v", day.Workouts)
	}
	arch, err := s.ArchiveWorkout(ctx, uid, w.WorkoutID)
	if err != nil || !arch.Archived {
		t.Fatalf("archive = %+v, %v", arch, err)
	}
	if arch, _ := s.ArchiveWorkout(ctx, uid, w.WorkoutID); arch.Archived {
		t.Error("archived twice")
	}
	_, err = s.LogWorkout(ctx, uid, LogWorkoutInput{ClientRequestID: "req-run-001", Type: "run", DurationMinutes: 30})
	wantUserErr(t, err, "archived")
	for _, b := range []struct {
		in   LogWorkoutInput
		want string
	}{
		{LogWorkoutInput{ClientRequestID: "x"}, "client_request_id"},
		{LogWorkoutInput{ClientRequestID: "req-w-00001"}, "type"},
		{LogWorkoutInput{ClientRequestID: "req-w-00002", Type: "dance", DurationMinutes: 1}, "type must"},
		{LogWorkoutInput{ClientRequestID: "req-w-00003", Type: "run"}, "duration"},
		{LogWorkoutInput{ClientRequestID: "req-w-00004", Type: "run", DurationMinutes: 1, Date: "x"}, "YYYY-MM-DD"},
		{LogWorkoutInput{ClientRequestID: "req-w-00005", Type: "run", DurationMinutes: 1, Note: ptr("")}, "note"},
	} {
		_, err := s.LogWorkout(ctx, uid, b.in)
		wantUserErr(t, err, b.want)
	}
	_, err = s.UpdateWorkout(ctx, uid, UpdateWorkoutInput{WorkoutID: w.WorkoutID})
	wantUserErr(t, err, "No workout")
	_, err = s.UpdateWorkout(ctx, uid, UpdateWorkoutInput{WorkoutID: w.WorkoutID, Note: ptr("")})
	wantUserErr(t, err, "note")

	set, err := s.UpdateSettings(ctx, uid, UpdateSettingsInput{TimeZone: "Asia/Tokyo", Targets: &TargetsInput{Kcal: ptr(2000), FatMinG: ptr(40), FatMaxG: ptr(70)}})
	if err != nil || set.TimeZone != "Asia/Tokyo" || *set.Targets.Kcal != 2000 {
		t.Fatalf("settings = %+v, %v", set, err)
	}
	set, err = s.UpdateSettings(ctx, uid, UpdateSettingsInput{ClearTargets: []TargetField{"kcal"}, Targets: &TargetsInput{WaterMl: ptr(2500)}})
	if err != nil || set.Targets.Kcal != nil || *set.Targets.FatMaxG != 70 || *set.Targets.WaterMl != 2500 {
		t.Fatalf("clear = %+v, %v", set, err)
	}
	if day, _ := s.GetDay(ctx, uid, ""); day.Date != "2026-03-15" {
		t.Errorf("today in Tokyo = %s", day.Date)
	}
	for _, b := range []struct {
		in   UpdateSettingsInput
		want string
	}{
		{UpdateSettingsInput{}, "Send"},
		{UpdateSettingsInput{TimeZone: "Mars/Olympus"}, "IANA"},
		{UpdateSettingsInput{TimeZone: "Local"}, "IANA"},
		{UpdateSettingsInput{Targets: &TargetsInput{Kcal: ptr(-1)}}, "0 to 100000"},
		{UpdateSettingsInput{ClearTargets: []TargetField{"steps"}}, "clear_targets"},
		{UpdateSettingsInput{Targets: &TargetsInput{FatMaxG: ptr(10)}}, "fat_max_g"},
	} {
		_, err := s.UpdateSettings(ctx, uid, b.in)
		wantUserErr(t, err, b.want)
	}
}

func TestErrors(t *testing.T) {
	if IsUserError(errors.New("x")) || !IsUserError(fmt.Errorf("wrap: %w", fail("y"))) {
		t.Error("IsUserError")
	}
}
