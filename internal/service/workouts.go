package service

import (
	"context"
	"errors"
	"slices"
	"time"

	"go.icco.me/foods/internal/nutrition"
	"go.icco.me/foods/internal/store"
)

// LogWorkoutInput is the input of log_workout.
type LogWorkoutInput struct {
	ClientRequestID string            `json:"client_request_id" jsonschema:"A unique id you make up for this request (for example a UUID). Re-send the same id when retrying; a repeat returns the first workout instead of logging another."`
	Type            store.WorkoutType `json:"type" jsonschema:"The kind of workout. Use other, with the details in note, if none fit."`
	DurationMinutes float64           `json:"duration_minutes" jsonschema:"Duration in minutes, for example 32.5."`
	Date            string            `json:"date,omitempty" jsonschema:"The day of the workout, YYYY-MM-DD in the user's time zone. Defaults to today."`
	Note            *string           `json:"note,omitempty" jsonschema:"A free-text note, for example 'easy pace'."`
}

// LoggedWorkout is the output of the workout tools.
type LoggedWorkout struct {
	WorkoutView
	AlreadyLogged bool
	Archived      bool
}

func checkWorkout(t store.WorkoutType, minutes *float64, date string) error {
	if t != "" && !slices.Contains(store.WorkoutTypes, t) {
		return fail("type must be run, walk, cycle, swim, strength, yoga, hiit or other.")
	}
	if minutes != nil && (*minutes <= 0 || *minutes > 1440) {
		return fail("duration_minutes must be greater than 0 and at most 1440.")
	}
	if date != "" && !validDate(date) {
		return fail("Date must be YYYY-MM-DD.")
	}
	return nil
}

func (s *Service) seenWorkout(ctx context.Context, userID, requestID string) (*LoggedWorkout, error) {
	r, err := s.Store.FindRequest(ctx, userID, requestID)
	if err != nil || r == nil {
		return nil, err
	}
	w, err := s.Store.GetWorkout(ctx, userID, r.IDs[0])
	if errors.Is(err, store.ErrNotFound) {
		return nil, fail("This request was already logged, and that workout has since been archived. Use a new client_request_id to log it again.")
	}
	if err != nil {
		return nil, err
	}
	return &LoggedWorkout{WorkoutView: workoutView(w), AlreadyLogged: true}, nil
}

// LogWorkout saves a workout. A request id seen before returns the first one.
func (s *Service) LogWorkout(ctx context.Context, userID string, in LogWorkoutInput) (*LoggedWorkout, error) {
	if err := checkRequestID(in.ClientRequestID); err != nil {
		return nil, err
	}
	if in.Type == "" {
		return nil, fail("type is required.")
	}
	if err := checkWorkout(in.Type, &in.DurationMinutes, in.Date); err != nil {
		return nil, err
	}
	if err := checkNote(in.Note); err != nil {
		return nil, err
	}
	if prior, err := s.seenWorkout(ctx, userID, in.ClientRequestID); prior != nil || err != nil {
		return prior, err
	}
	date := in.Date
	if date == "" {
		loc, err := s.location(ctx, userID)
		if err != nil {
			return nil, err
		}
		date = s.Now().In(loc).Format(time.DateOnly)
	}
	w := &store.Workout{Date: date, Type: in.Type, DurationMinutes: in.DurationMinutes, Note: in.Note}
	if err := s.Store.CreateWorkout(ctx, userID, in.ClientRequestID, w); err != nil {
		if errors.Is(err, store.ErrDuplicate) {
			return s.seenWorkout(ctx, userID, in.ClientRequestID)
		}
		return nil, err
	}
	return &LoggedWorkout{WorkoutView: workoutView(w)}, nil
}

// WorkoutField is a workout field that update_workout can remove.
type WorkoutField string

// UpdateWorkoutInput is the input of update_workout.
type UpdateWorkoutInput struct {
	WorkoutID       string            `json:"workout_id" jsonschema:"workout_id from get_day or log_workout."`
	Type            store.WorkoutType `json:"type,omitempty" jsonschema:"The kind of workout."`
	DurationMinutes *float64          `json:"duration_minutes,omitempty" jsonschema:"Duration in minutes."`
	Date            string            `json:"date,omitempty" jsonschema:"Move to this day, YYYY-MM-DD in the user's time zone."`
	Note            *string           `json:"note,omitempty" jsonschema:"New note."`
	Clear           []WorkoutField    `json:"clear,omitempty" jsonschema:"Fields to remove: note."`
}

// UpdateWorkout changes a workout. Only the fields sent change.
func (s *Service) UpdateWorkout(ctx context.Context, userID string, in UpdateWorkoutInput) (*LoggedWorkout, error) {
	if err := checkWorkout(in.Type, in.DurationMinutes, in.Date); err != nil {
		return nil, err
	}
	if err := checkNote(in.Note); err != nil {
		return nil, err
	}
	if _, err := s.Store.GetWorkout(ctx, userID, in.WorkoutID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, fail("No workout with that id. Use get_day to see workout_ids.")
		}
		return nil, err
	}
	c := map[string]any{}
	if in.Type != "" {
		c["type"] = in.Type
	}
	if in.DurationMinutes != nil {
		c["duration_minutes"] = *in.DurationMinutes
	}
	if in.Date != "" {
		c["date"] = in.Date
	}
	if in.Note != nil {
		c["note"] = *in.Note
	}
	for _, f := range in.Clear {
		if f != "note" {
			return nil, fail("clear may list note.")
		}
		c["note"] = nil
	}
	var w store.Workout
	if err := s.Store.UpdateRow(ctx, userID, in.WorkoutID, &w, c); err != nil {
		return nil, err
	}
	return &LoggedWorkout{WorkoutView: workoutView(&w)}, nil
}

// WorkoutIDInput names one workout.
type WorkoutIDInput struct {
	WorkoutID string `json:"workout_id" jsonschema:"workout_id from get_day or log_workout."`
}

// ArchiveWorkout hides a workout from its day. One already archived is not
// an error.
func (s *Service) ArchiveWorkout(ctx context.Context, userID, workoutID string) (*LoggedWorkout, error) {
	w, err := s.Store.GetWorkout(ctx, userID, workoutID)
	if errors.Is(err, store.ErrNotFound) {
		return &LoggedWorkout{}, nil
	}
	if err != nil {
		return nil, err
	}
	ok, err := s.Store.Archive(ctx, userID, workoutID, &store.Workout{})
	if err != nil || !ok {
		return &LoggedWorkout{}, err
	}
	return &LoggedWorkout{WorkoutView: workoutView(w), Archived: true}, nil
}

// TargetsInput are daily targets. A field not sent keeps its value.
type TargetsInput struct {
	Kcal        *int `json:"kcal,omitempty" jsonschema:"Daily calories."`
	ProteinG    *int `json:"protein_g,omitempty" jsonschema:"Daily protein goal in grams."`
	CarbsG      *int `json:"carbs_g,omitempty" jsonschema:"Daily carbs goal in grams."`
	FiberG      *int `json:"fiber_g,omitempty" jsonschema:"Daily fiber goal in grams."`
	FatMinG     *int `json:"fat_min_g,omitempty" jsonschema:"Minimum fat in grams."`
	FatMaxG     *int `json:"fat_max_g,omitempty" jsonschema:"Maximum fat in grams (get_day reports fat left against this)."`
	SodiumMaxMg *int `json:"sodium_max_mg,omitempty" jsonschema:"Maximum sodium in milligrams."`
	WaterMl     *int `json:"water_ml,omitempty" jsonschema:"Daily water goal in milliliters."`
}

// TargetField names one target.
type TargetField string

// UpdateSettingsInput is the input of update_settings.
type UpdateSettingsInput struct {
	TimeZone     string        `json:"time_zone,omitempty" jsonschema:"IANA time zone name, for example America/Chicago."`
	Targets      *TargetsInput `json:"targets,omitempty" jsonschema:"Daily targets to set."`
	ClearTargets []TargetField `json:"clear_targets,omitempty" jsonschema:"Targets to remove."`
}

// Settings is the output of update_settings.
type Settings struct {
	TimeZone string
	Targets  nutrition.Targets
}

// UpdateSettings sets the time zone and daily targets.
func (s *Service) UpdateSettings(ctx context.Context, userID string, in UpdateSettingsInput) (*Settings, error) {
	if in.TimeZone == "" && in.Targets == nil && len(in.ClearTargets) == 0 {
		return nil, fail("Send a time_zone, targets, clear_targets, or a mix.")
	}
	if in.TimeZone != "" {
		if _, err := time.LoadLocation(in.TimeZone); err != nil || in.TimeZone == "Local" {
			return nil, fail("time_zone must be an IANA name such as America/Chicago or Europe/London.")
		}
	}
	t, err := s.Store.GetTargets(ctx, userID)
	if err != nil {
		return nil, err
	}
	if t == nil {
		t = &store.Target{}
	}
	fields := map[TargetField]**int{
		"kcal": &t.Kcal, "protein_g": &t.ProteinG, "carbs_g": &t.CarbsG, "fiber_g": &t.FiberG,
		"fat_min_g": &t.FatMinG, "fat_max_g": &t.FatMaxG, "sodium_max_mg": &t.SodiumMaxMg, "water_ml": &t.WaterMl,
	}
	if in.Targets != nil {
		g := in.Targets
		for k, v := range map[TargetField]*int{
			"kcal": g.Kcal, "protein_g": g.ProteinG, "carbs_g": g.CarbsG, "fiber_g": g.FiberG,
			"fat_min_g": g.FatMinG, "fat_max_g": g.FatMaxG, "sodium_max_mg": g.SodiumMaxMg, "water_ml": g.WaterMl,
		} {
			if v == nil {
				continue
			}
			if *v < 0 || *v > 100000 {
				return nil, fail("Targets must be 0 to 100000.")
			}
			*fields[k] = v
		}
	}
	for _, k := range in.ClearTargets {
		p, ok := fields[k]
		if !ok {
			return nil, fail("clear_targets may list kcal, protein_g, carbs_g, fiber_g, fat_min_g, fat_max_g, sodium_max_mg or water_ml.")
		}
		*p = nil
	}
	if t.FatMinG != nil && t.FatMaxG != nil && *t.FatMaxG < *t.FatMinG {
		return nil, fail("fat_max_g cannot be below fat_min_g.")
	}
	if in.TimeZone != "" {
		if err := s.Store.SetTimeZone(ctx, userID, in.TimeZone); err != nil {
			return nil, err
		}
	}
	if in.Targets != nil || len(in.ClearTargets) > 0 {
		if err := s.Store.SaveTargets(ctx, userID, t); err != nil {
			return nil, err
		}
	}
	u, err := s.Store.GetUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	return &Settings{TimeZone: u.TimeZone, Targets: t.Nutrition()}, nil
}
