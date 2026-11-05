// Package service holds the rules that need the database. Tool handlers call
// these; SQL stays in the store.
package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.icco.me/foods/internal/foodapi"
	"go.icco.me/foods/internal/store"
)

// Error is a problem the assistant can fix by changing its request. Its
// message is shown to the assistant as is.
type Error struct {
	Msg string
}

func (e *Error) Error() string { return e.Msg }

func fail(format string, a ...any) error {
	return &Error{Msg: fmt.Sprintf(format, a...)}
}

// IsUserError reports whether err is an Error.
func IsUserError(err error) bool {
	var e *Error
	return errors.As(err, &e)
}

// FoodDatabases searches the outside food databases.
type FoodDatabases interface {
	Search(ctx context.Context, s foodapi.Source, query string, limit int) ([]foodapi.Food, error)
	Get(ctx context.Context, s foodapi.Source, id string) (*foodapi.Food, error)
}

// Service runs every tool's rules.
type Service struct {
	Store *store.Store
	Foods FoodDatabases
	Now   func() time.Time
}

// New returns a Service using the real clock.
func New(st *store.Store, foods FoodDatabases) *Service {
	return &Service{Store: st, Foods: foods, Now: time.Now}
}

func (s *Service) location(ctx context.Context, userID string) (*time.Location, error) {
	u, err := s.Store.GetUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	loc, err := time.LoadLocation(u.TimeZone)
	if err != nil {
		return time.LoadLocation(store.DefaultTimeZone)
	}
	return loc, nil
}

func validDate(s string) bool {
	t, err := time.Parse(time.DateOnly, s)
	return err == nil && t.Format(time.DateOnly) == s
}

// when works out a log's day and time. eaten_at wins, and its day is taken
// in the user's time zone. A date alone means noon that day. Neither means now.
func when(loc *time.Location, now time.Time, date, eatenAt string) (string, time.Time, error) {
	if date != "" && !validDate(date) {
		return "", time.Time{}, fail("Date must be YYYY-MM-DD.")
	}
	if eatenAt != "" {
		t, err := time.Parse(time.RFC3339, eatenAt)
		if err != nil {
			return "", time.Time{}, fail("eaten_at must be an ISO 8601 time with an offset, for example 2026-03-14T08:30:00-05:00.")
		}
		return t.In(loc).Format(time.DateOnly), t.UTC(), nil
	}
	if date != "" {
		d, _ := time.ParseInLocation(time.DateOnly, date, loc)
		return date, time.Date(d.Year(), d.Month(), d.Day(), 12, 0, 0, 0, loc).UTC(), nil
	}
	return now.In(loc).Format(time.DateOnly), now.UTC(), nil
}

func checkNote(n *string) error {
	if n != nil && (len(*n) < 1 || len(*n) > 2000) {
		return fail("note must be 1 to 2000 characters.")
	}
	return nil
}

func checkRequestID(id string) error {
	if len(id) < 8 || len(id) > 64 {
		return fail("client_request_id must be 8 to 64 characters. Make up a new one, such as a UUID, for each new request.")
	}
	return nil
}
