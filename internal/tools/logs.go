package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.icco.me/foods/internal/nutrition"
	"go.icco.me/foods/internal/service"
)

func nut(n nutrition.Nutrients) string {
	return fmt.Sprintf("%s kcal, %s g protein", num(n.Kcal), num(n.ProteinG))
}

func leftSummary(d nutrition.DayTotals) string {
	parts := []string{fmt.Sprintf("Day total so far: %s.", nut(d.Eaten))}
	if v, ok := d.RemainingValue("kcal"); ok {
		parts = append(parts, fmt.Sprintf("%s kcal left.", num(v)))
	}
	if v, ok := d.RemainingValue("protein_g"); ok {
		parts = append(parts, fmt.Sprintf("%s g protein left.", num(v)))
	}
	return strings.Join(parts, " ")
}

func daySummary(d *service.Day) string {
	lines := []string{fmt.Sprintf("%s and %s on %s.", plural(len(d.FoodLogs), "food entry", "food entries"), plural(len(d.Workouts), "workout", "workouts"), d.Date)}
	for _, l := range d.FoodLogs {
		state := "eaten"
		if !l.Eaten {
			state = "planned"
		}
		extra := ""
		if l.Note != nil {
			extra += fmt.Sprintf(" note: %q", *l.Note)
		}
		if l.MindfulnessScore != nil {
			extra += " mindfulness " + num(*l.MindfulnessScore) + "/10"
		}
		lines = append(lines, fmt.Sprintf("- %s: %s g %s (%s) %s%s [log_id=%s]", l.MealType, num(l.Grams), l.FoodName, state, nut(l.Nutrients), extra, l.LogID))
	}
	for _, w := range d.Workouts {
		note := ""
		if w.Note != nil {
			note = " (" + *w.Note + ")"
		}
		lines = append(lines, fmt.Sprintf("- workout: %s min %s%s [workout_id=%s]", num(w.DurationMinutes), w.Type, note, w.WorkoutID))
	}
	e := d.Totals.Eaten
	lines = append(lines, fmt.Sprintf("Eaten: %s kcal, %s g protein, %s g fat, %s g carbs, %s ml water.", num(e.Kcal), num(e.ProteinG), num(e.FatG), num(e.CarbsG), num(d.Totals.WaterMl)))
	if p := d.Totals.Planned; p != e {
		lines = append(lines, fmt.Sprintf("Including planned: %s.", nut(p)))
	}
	var left []string
	for _, r := range d.Totals.Remaining {
		left = append(left, fmt.Sprintf("%s %s", num(r.Value), r.Key))
	}
	if len(left) > 0 {
		lines = append(lines, "Left today (negative is over): "+strings.Join(left, ", ")+".")
	}
	return strings.Join(lines, "\n")
}

func registerLogs(srv *mcp.Server, svc *service.Service) {
	add(srv, "log_entries", "Log food and water",
		"Log what the user ate or drank, or plans to eat (eaten=false). Foods: entries, each a food_id (from search_foods) with grams, or with servings (1 = one serving of a saved meal or packaged food; 0.5 = half), under one meal_type. Drinking water: just send water_ml (for example 500); the server files it under meal type water. Entries and water_ml can be sent together. All entries are saved together or not at all. Always send a client_request_id: a new unique value for each new request, and the SAME value if you retry, so a retry is not saved twice. Returns the day's new totals, calculated by the server.",
		addsOnce, svc.LogEntries, func(r *service.Logged) string {
			prefix := ""
			if r.AlreadyLogged {
				prefix = "Already logged by an earlier request with this client_request_id (nothing new saved). "
			}
			return fmt.Sprintf("%sLogged %s. log_ids=[%s]. %s", prefix, plural(len(r.LogIDs), "entry", "entries"), strings.Join(r.LogIDs, ", "), leftSummary(r.Day))
		})
	add(srv, "update_log", "Change a logged entry",
		"Change one log (get its log_id from get_day): grams, meal_type, date, eaten_at, note, eaten or mindfulness_score; clear removes note or mindfulness_score. Only the fields you send change. Use eaten=true to mark a planned entry as eaten. To change which food a log is for, archive it with archive_log and log again.",
		changes, svc.UpdateLog, func(r *service.Changed) string {
			return fmt.Sprintf("Updated: %s g %s %s on %s. %s", num(r.Log.Grams), r.Log.FoodName, nut(r.Log.Nutrients), r.Date, leftSummary(r.Day))
		})
	add(srv, "archive_log", "Archive a logged entry",
		"Remove one food log from the day by archiving it (get its log_id from get_day). Nothing is deleted: the entry is kept but left out of the day and every total. If the log is already archived it says so; that is not an error.",
		changes, func(ctx context.Context, uid string, in service.LogIDInput) (*service.Changed, error) {
			return svc.ArchiveLog(ctx, uid, in.LogID)
		}, func(r *service.Changed) string {
			if !r.Archived {
				return "That log is already archived (or never existed). Nothing changed."
			}
			return fmt.Sprintf("Archived %s g %s (%s, %s). %s", num(r.Log.Grams), r.Log.FoodName, r.Log.MealType, r.Date, leftSummary(r.Day))
		})
	add(srv, "get_day", "Get a day",
		"One day's food logs (each with meal_type and log_id), workouts (each with workout_id) and totals: eaten, planned, water, targets and what is left. Defaults to today in the user's time zone.",
		reads, func(ctx context.Context, uid string, in service.GetDayInput) (*service.Day, error) {
			return svc.GetDay(ctx, uid, in.Date)
		}, daySummary)
}

func registerWorkouts(srv *mcp.Server, svc *service.Service) {
	add(srv, "log_workout", "Log a workout",
		"Log a workout: a type (run, walk, cycle, swim, strength, yoga, hiit or other) and its duration in minutes. date defaults to today in the user's time zone. There is no calorie or distance field. Always send a client_request_id: a new unique value for each new workout, and the SAME value if you retry, so a retry is not saved twice.",
		addsOnce, svc.LogWorkout, func(r *service.LoggedWorkout) string {
			prefix := ""
			if r.AlreadyLogged {
				prefix = "Already logged by an earlier request with this client_request_id (nothing new saved). "
			}
			return fmt.Sprintf("%sLogged %s min of %s on %s. workout_id=%s", prefix, num(r.DurationMinutes), r.Type, r.Date, r.WorkoutID)
		})
	add(srv, "update_workout", "Change a workout",
		"Change a workout (get its workout_id from get_day): type, duration_minutes, date or note; clear removes the note. Only the fields you send change.",
		changes, svc.UpdateWorkout, func(r *service.LoggedWorkout) string {
			return fmt.Sprintf("Updated: %s min of %s on %s.", num(r.DurationMinutes), r.Type, r.Date)
		})
	add(srv, "archive_workout", "Archive a workout",
		"Remove one workout from the day by archiving it (workout_id from get_day). Nothing is deleted: the workout is kept but left out of the day. If it is already archived it says so; that is not an error.",
		changes, func(ctx context.Context, uid string, in service.WorkoutIDInput) (*service.LoggedWorkout, error) {
			return svc.ArchiveWorkout(ctx, uid, in.WorkoutID)
		}, func(r *service.LoggedWorkout) string {
			if !r.Archived {
				return "That workout is already archived (or never existed). Nothing changed."
			}
			return fmt.Sprintf("Archived the %s min %s workout on %s.", num(r.DurationMinutes), r.Type, r.Date)
		})
	add(srv, "update_settings", "Change settings",
		"Set the user's time zone and/or daily targets. The time zone decides what 'today' is and which day each log lands on, so set it first. Targets are what get_day measures against. Only the fields you send change; clear_targets removes targets.",
		changes, svc.UpdateSettings, func(r *service.Settings) string {
			t := r.Targets
			var set []string
			for _, kv := range []struct {
				k string
				v *int
			}{{"kcal", t.Kcal}, {"protein_g", t.ProteinG}, {"carbs_g", t.CarbsG}, {"fiber_g", t.FiberG}, {"fat_min_g", t.FatMinG}, {"fat_max_g", t.FatMaxG}, {"sodium_max_mg", t.SodiumMaxMg}, {"water_ml", t.WaterMl}} {
				if kv.v != nil {
					set = append(set, fmt.Sprintf("%s=%d", kv.k, *kv.v))
				}
			}
			targets := "none set"
			if len(set) > 0 {
				targets = strings.Join(set, ", ")
			}
			return fmt.Sprintf("Time zone %s. Targets: %s.", r.TimeZone, targets)
		})
}
