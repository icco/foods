package service

import (
	"context"
	"errors"
	"slices"
	"time"

	"go.icco.me/foods/internal/nutrition"
	"go.icco.me/foods/internal/store"
)

// LogView is one food log with its computed nutrients.
type LogView struct {
	LogID            string
	MealType         store.MealType
	FoodID           string
	FoodName         string
	Grams            float64
	Note             *string
	Eaten            bool
	MindfulnessScore *float64
	EatenAt          time.Time
	Nutrients        nutrition.Nutrients
}

// WorkoutView is one workout.
type WorkoutView struct {
	WorkoutID       string
	Date            string
	Type            store.WorkoutType
	DurationMinutes float64
	Note            *string
}

func workoutView(w *store.Workout) WorkoutView {
	return WorkoutView{WorkoutID: w.ID, Date: w.Date, Type: w.Type, DurationMinutes: w.DurationMinutes, Note: w.Note}
}

// Day is the output of get_day.
type Day struct {
	Date     string
	FoodLogs []LogView
	Workouts []WorkoutView
	Totals   nutrition.DayTotals
}

func logView(l *store.FoodLog) LogView {
	v := LogView{
		LogID: l.ID, MealType: l.MealType, FoodID: l.FoodID, FoodName: l.Food.Name, Grams: l.Grams, Note: l.Note, Eaten: l.Eaten, EatenAt: l.EatenAt,
		Nutrients: nutrition.Round(nutrition.Scale(l.Food.Values(), l.Grams)),
	}
	if l.MindfulnessScore != nil {
		m := nutrition.MindfulnessFromStored(*l.MindfulnessScore)
		v.MindfulnessScore = &m
	}
	return v
}

// GetDayInput is the input of get_day.
type GetDayInput struct {
	Date string `json:"date,omitempty" jsonschema:"A calendar day, YYYY-MM-DD, in the user's time zone. Defaults to today."`
}

// GetDay returns one day's logs, workouts and totals. The day defaults to
// today in the user's time zone.
func (s *Service) GetDay(ctx context.Context, userID, date string) (*Day, error) {
	if date == "" {
		loc, err := s.location(ctx, userID)
		if err != nil {
			return nil, err
		}
		date = s.Now().In(loc).Format(time.DateOnly)
	}
	if !validDate(date) {
		return nil, fail("Date must be YYYY-MM-DD.")
	}
	logs, err := s.Store.DayFoodLogs(ctx, userID, date)
	if err != nil {
		return nil, err
	}
	targets, err := s.Store.GetTargets(ctx, userID)
	if err != nil {
		return nil, err
	}
	workouts, err := s.Store.DayWorkouts(ctx, userID, date)
	if err != nil {
		return nil, err
	}
	d := &Day{Date: date}
	var entries []nutrition.Entry
	water := 0.0
	for _, l := range logs {
		d.FoodLogs = append(d.FoodLogs, logView(l))
		entries = append(entries, nutrition.Entry{Eaten: l.Eaten, Nutrients: nutrition.Scale(l.Food.Values(), l.Grams)})
		if l.MealType == "water" && l.Eaten {
			water += l.Grams
		}
	}
	for _, w := range workouts {
		d.Workouts = append(d.Workouts, workoutView(w))
	}
	d.Totals = nutrition.Day(date, entries, targets.Nutrition(), water)
	return d, nil
}

// EntryInput is one food, as grams or as servings.
type EntryInput struct {
	FoodID   string   `json:"food_id" jsonschema:"food_id from search_foods."`
	Grams    *float64 `json:"grams,omitempty" jsonschema:"Amount in grams, 0 to 5000. Give grams or servings."`
	Servings *float64 `json:"servings,omitempty" jsonschema:"Number of servings, for example 1 or 0.5. Needs the food to have a serving size."`
}

// LogEntriesInput is the input of log_entries.
type LogEntriesInput struct {
	ClientRequestID  string         `json:"client_request_id" jsonschema:"A unique id you make up for this request (for example a UUID). Re-send the same id when retrying after an error or timeout; a repeat returns the first result instead of logging again."`
	MealType         store.MealType `json:"meal_type,omitempty" jsonschema:"breakfast, lunch, dinner, snack or dessert. Required with entries. Do not use water here; use water_ml."`
	Entries          []EntryInput   `json:"entries,omitempty" jsonschema:"The foods, 1 to 30."`
	WaterMl          *float64       `json:"water_ml,omitempty" jsonschema:"Drinking water in milliliters, for example 500."`
	Date             string         `json:"date,omitempty" jsonschema:"The day this was eaten, YYYY-MM-DD in the user's time zone (noon is assumed). Use this when the user names a day but not a time. Defaults to now."`
	EatenAt          string         `json:"eaten_at,omitempty" jsonschema:"When it was eaten: an ISO 8601 time with offset, for example 2026-03-14T08:30:00-05:00. Wins over date. Defaults to now."`
	Note             *string        `json:"note,omitempty" jsonschema:"A free-text note, for example 'at the diner'. Applies to every entry."`
	Eaten            *bool          `json:"eaten,omitempty" jsonschema:"true (default) means it was eaten. false means planned, not yet eaten: it counts in planned totals but not eaten totals."`
	MindfulnessScore *float64       `json:"mindfulness_score,omitempty" jsonschema:"How mindfully it was eaten, 1 (distracted) to 10 (fully attentive). Only if the user says so."`
}

// Logged is the output of log_entries.
type Logged struct {
	LogIDs        []string
	Day           nutrition.DayTotals
	AlreadyLogged bool
}

func checkScore(m *float64) (*float64, error) {
	if m == nil {
		return nil, nil
	}
	if *m < 1 || *m > 10 {
		return nil, fail("mindfulness_score must be 1 to 10.")
	}
	v := nutrition.MindfulnessToStored(*m)
	return &v, nil
}

func (s *Service) seen(ctx context.Context, userID string, r *store.RequestLog) (*Logged, error) {
	d, err := s.GetDay(ctx, userID, r.Date)
	if err != nil {
		return nil, err
	}
	return &Logged{LogIDs: r.IDs, Day: d.Totals, AlreadyLogged: true}, nil
}

// LogEntries logs foods under one meal type, and water, all or nothing. A
// request id seen before returns the first result without saving again.
func (s *Service) LogEntries(ctx context.Context, userID string, in LogEntriesInput) (*Logged, error) {
	if err := checkRequestID(in.ClientRequestID); err != nil {
		return nil, err
	}
	if r, err := s.Store.FindRequest(ctx, userID, in.ClientRequestID); err != nil || r != nil {
		if err != nil {
			return nil, err
		}
		return s.seen(ctx, userID, r)
	}
	switch {
	case len(in.Entries) == 0 && in.WaterMl == nil:
		return nil, fail("Send entries (food_id with grams or servings), water_ml, or both.")
	case len(in.Entries) > 30:
		return nil, fail("Send at most 30 entries.")
	case len(in.Entries) > 0 && in.MealType == "":
		return nil, fail("meal_type is required with entries (breakfast, lunch, dinner, snack or dessert).")
	case in.MealType != "" && !slices.Contains(store.MealTypes, in.MealType):
		return nil, fail("meal_type must be breakfast, lunch, dinner, snack, dessert or water.")
	case in.MealType == "water" && len(in.Entries) > 0 && in.WaterMl != nil:
		return nil, fail("Use water_ml for water, and meal_type for the other foods.")
	}
	if err := checkNote(in.Note); err != nil {
		return nil, err
	}
	score, err := checkScore(in.MindfulnessScore)
	if err != nil {
		return nil, err
	}
	loc, err := s.location(ctx, userID)
	if err != nil {
		return nil, err
	}
	date, at, err := when(loc, s.Now(), in.Date, in.EatenAt)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(in.Entries))
	for i, e := range in.Entries {
		ids[i] = e.FoodID
	}
	foods, err := s.Store.GetFoods(ctx, userID, ids)
	if err != nil {
		return nil, err
	}
	eaten := in.Eaten == nil || *in.Eaten
	row := func(foodID string, grams float64, meal store.MealType) *store.FoodLog {
		return &store.FoodLog{Date: date, EatenAt: at, MealType: meal, FoodID: foodID, Grams: grams, Note: in.Note, Eaten: eaten, MindfulnessScore: score}
	}
	var logs []*store.FoodLog
	for i, e := range in.Entries {
		food := foods[e.FoodID]
		if food == nil {
			return nil, fail(`No food with that id (entries[%d]). Use search_foods to get a food_id (a result marked "not saved" needs import_food first).`, i)
		}
		if (e.Grams == nil) == (e.Servings == nil) {
			return nil, fail("entries[%d] needs exactly one of grams or servings.", i)
		}
		var grams float64
		if e.Grams != nil {
			grams = *e.Grams
		} else {
			if food.ServingG == nil {
				return nil, fail("%s has no serving size, so give grams (entries[%d]). Or set one with update_food (serving_g).", food.Name, i)
			}
			grams = *e.Servings * *food.ServingG
		}
		if grams <= 0 || grams > 5000 {
			return nil, fail("The amount must be greater than 0 and at most 5000 g (entries[%d]).", i)
		}
		logs = append(logs, row(food.ID, grams, in.MealType))
	}
	if in.WaterMl != nil {
		if *in.WaterMl <= 0 || *in.WaterMl > 5000 {
			return nil, fail("water_ml must be greater than 0 and at most 5000.")
		}
		water, err := s.Store.FindWaterFood(ctx, userID)
		if err != nil {
			return nil, err
		}
		if water == nil {
			return nil, fail(`There is no food named "Water". Add one with add_food (all nutrients 0), then log it with meal_type water.`)
		}
		logs = append(logs, row(water.ID, *in.WaterMl, "water"))
	}
	created, err := s.Store.CreateFoodLogs(ctx, userID, in.ClientRequestID, date, logs)
	if errors.Is(err, store.ErrDuplicate) {
		r, ferr := s.Store.FindRequest(ctx, userID, in.ClientRequestID)
		if ferr != nil || r == nil {
			return nil, errors.Join(err, ferr)
		}
		return s.seen(ctx, userID, r)
	}
	if err != nil {
		return nil, err
	}
	d, err := s.GetDay(ctx, userID, date)
	if err != nil {
		return nil, err
	}
	return &Logged{LogIDs: created, Day: d.Totals}, nil
}

// LogField is a log field that update_log can remove.
type LogField string

// UpdateLogInput is the input of update_log.
type UpdateLogInput struct {
	LogID            string         `json:"log_id" jsonschema:"log_id from get_day or log_entries."`
	Grams            *float64       `json:"grams,omitempty" jsonschema:"New amount in grams."`
	MealType         store.MealType `json:"meal_type,omitempty" jsonschema:"breakfast, lunch, dinner, snack, dessert or water."`
	Date             string         `json:"date,omitempty" jsonschema:"Move to this day, YYYY-MM-DD in the user's time zone (noon is assumed)."`
	EatenAt          string         `json:"eaten_at,omitempty" jsonschema:"New time: an ISO 8601 time with offset. Wins over date."`
	Note             *string        `json:"note,omitempty" jsonschema:"New note."`
	Eaten            *bool          `json:"eaten,omitempty" jsonschema:"true = eaten, false = planned."`
	MindfulnessScore *float64       `json:"mindfulness_score,omitempty" jsonschema:"1 (distracted) to 10 (fully attentive)."`
	Clear            []LogField     `json:"clear,omitempty" jsonschema:"Fields to remove: note or mindfulness_score."`
}

// Changed is the output of update_log and archive_log.
type Changed struct {
	Log      LogView
	Date     string
	Day      nutrition.DayTotals
	Archived bool
}

// UpdateLog changes one log. Only the fields sent change.
func (s *Service) UpdateLog(ctx context.Context, userID string, in UpdateLogInput) (*Changed, error) {
	c := map[string]any{}
	if in.Grams != nil {
		if *in.Grams <= 0 || *in.Grams > 5000 {
			return nil, fail("grams must be greater than 0 and at most 5000.")
		}
		c["grams"] = *in.Grams
	}
	if in.MealType != "" {
		if !slices.Contains(store.MealTypes, in.MealType) {
			return nil, fail("meal_type must be breakfast, lunch, dinner, snack, dessert or water.")
		}
		c["meal_type"] = in.MealType
	}
	if err := checkNote(in.Note); err != nil {
		return nil, err
	}
	if in.Note != nil {
		c["note"] = *in.Note
	}
	if in.Eaten != nil {
		c["eaten"] = *in.Eaten
	}
	score, err := checkScore(in.MindfulnessScore)
	if err != nil {
		return nil, err
	}
	if score != nil {
		c["mindfulness_score"] = *score
	}
	for _, f := range in.Clear {
		if f != "note" && f != "mindfulness_score" {
			return nil, fail("clear may list note or mindfulness_score.")
		}
		c[string(f)] = nil
	}
	if in.Date != "" || in.EatenAt != "" {
		loc, err := s.location(ctx, userID)
		if err != nil {
			return nil, err
		}
		date, at, err := when(loc, s.Now(), in.Date, in.EatenAt)
		if err != nil {
			return nil, err
		}
		c["date"], c["eaten_at"] = date, at
	}
	if _, err := s.Store.GetFoodLog(ctx, userID, in.LogID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, fail("No log with that id. Use get_day to see log_ids.")
		}
		return nil, err
	}
	var l store.FoodLog
	if err := s.Store.UpdateRow(ctx, userID, in.LogID, &l, c); err != nil {
		return nil, err
	}
	full, err := s.Store.GetFoodLog(ctx, userID, l.ID)
	if err != nil {
		return nil, err
	}
	d, err := s.GetDay(ctx, userID, full.Date)
	if err != nil {
		return nil, err
	}
	return &Changed{Log: logView(full), Date: full.Date, Day: d.Totals}, nil
}

// LogIDInput names one log.
type LogIDInput struct {
	LogID string `json:"log_id" jsonschema:"log_id from get_day or log_entries."`
}

// ArchiveLog hides a log from every day and total. One already archived is
// not an error.
func (s *Service) ArchiveLog(ctx context.Context, userID, logID string) (*Changed, error) {
	l, err := s.Store.GetFoodLog(ctx, userID, logID)
	if errors.Is(err, store.ErrNotFound) {
		return &Changed{}, nil
	}
	if err != nil {
		return nil, err
	}
	ok, err := s.Store.Archive(ctx, userID, logID, &store.FoodLog{})
	if err != nil || !ok {
		return &Changed{}, err
	}
	d, err := s.GetDay(ctx, userID, l.Date)
	if err != nil {
		return nil, err
	}
	return &Changed{Log: logView(l), Date: l.Date, Day: d.Totals, Archived: true}, nil
}
