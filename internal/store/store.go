package store

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Store reads and writes food data.
type Store struct {
	db *gorm.DB
}

// New wraps a database handle.
func New(db *gorm.DB) *Store {
	return &Store{db: db}
}

// Migrate creates or updates every table.
func (s *Store) Migrate(ctx context.Context) error {
	return s.db.WithContext(ctx).AutoMigrate(Models...)
}

// ErrNotFound means the row is missing, archived, or belongs to someone else.
var ErrNotFound = errors.New("not found")

// ErrDuplicate means a unique constraint rejected the row.
var ErrDuplicate = errors.New("duplicate")

func newID() string { return uuid.NewString() }

func isUnique(err error) bool {
	return errors.Is(err, gorm.ErrDuplicatedKey) || (err != nil && strings.Contains(err.Error(), "SQLSTATE 23505"))
}

// FindOrCreateUser returns the user for an upstream identity, creating the
// user and their Water food on first sign-in. It records the latest email.
func (s *Store) FindOrCreateUser(ctx context.Context, provider, subject, email string) (*User, error) {
	var u User
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Where("provider = ? AND subject = ?", provider, subject).Limit(1).Find(&u)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected > 0 {
			if u.Email == email {
				return nil
			}
			u.Email = email
			return tx.Model(&u).Update("email", email).Error
		}
		u = User{ID: newID(), Provider: provider, Subject: subject, Email: email, TimeZone: DefaultTimeZone}
		if err := tx.Create(&u).Error; err != nil {
			return err
		}
		zero := 0.0
		return tx.Create(&Food{ID: newID(), UserID: u.ID, Name: WaterFoodName, FiberG: &zero, SodiumMg: &zero, Source: SourceLabel, Verified: true}).Error
	})
	if err != nil {
		return nil, fmt.Errorf("find or create user: %w", err)
	}
	return &u, nil
}

// GetUser returns one user.
func (s *Store) GetUser(ctx context.Context, userID string) (*User, error) {
	var u User
	res := s.db.WithContext(ctx).Where("id = ?", userID).Limit(1).Find(&u)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrNotFound
	}
	return &u, nil
}

// SetTimeZone changes a user's time zone.
func (s *Store) SetTimeZone(ctx context.Context, userID, tz string) error {
	return s.db.WithContext(ctx).Model(&User{}).Where("id = ?", userID).Update("time_zone", tz).Error
}

// CreateFood saves a food for a user.
func (s *Store) CreateFood(ctx context.Context, userID string, f *Food) error {
	f.ID, f.UserID = newID(), userID
	if err := s.db.WithContext(ctx).Create(f).Error; err != nil {
		if isUnique(err) {
			return ErrDuplicate
		}
		return err
	}
	return nil
}

// GetFood returns one of the user's foods, archived or not.
func (s *Store) GetFood(ctx context.Context, userID, foodID string) (*Food, error) {
	var f Food
	if uuid.Validate(foodID) != nil {
		return nil, ErrNotFound
	}
	res := s.db.WithContext(ctx).Where("id = ? AND user_id = ?", foodID, userID).Limit(1).Find(&f)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrNotFound
	}
	return &f, nil
}

// GetFoods returns the user's foods by id. Ids that are not theirs are missing
// from the map.
func (s *Store) GetFoods(ctx context.Context, userID string, ids []string) (map[string]*Food, error) {
	var valid []string
	for _, id := range ids {
		if uuid.Validate(id) == nil && !slices.Contains(valid, id) {
			valid = append(valid, id)
		}
	}
	out := map[string]*Food{}
	if len(valid) == 0 {
		return out, nil
	}
	var rows []*Food
	if err := s.db.WithContext(ctx).Where("user_id = ? AND id IN ?", userID, valid).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, f := range rows {
		out[f.ID] = f
	}
	return out, nil
}

var terms = regexp.MustCompile(`[\p{L}\p{N}]+`)

// SearchFoods matches every word of query against the name or brand of the
// user's active foods, shortest name first.
func (s *Store) SearchFoods(ctx context.Context, userID, query string, limit int) ([]*Food, error) {
	words := terms.FindAllString(query, -1)
	if len(words) == 0 {
		return nil, nil
	}
	q := s.db.WithContext(ctx).Where("user_id = ? AND archived_at IS NULL", userID)
	for _, w := range words {
		like := "%" + w + "%"
		q = q.Where("(name ILIKE ? OR brand ILIKE ?)", like, like)
	}
	var rows []*Food
	err := q.Order("length(name), name").Limit(limit).Find(&rows).Error
	return rows, err
}

// GetFoodByExternal returns the user's copy of an outside food, if saved.
func (s *Store) GetFoodByExternal(ctx context.Context, userID, source, externalID string) (*Food, error) {
	var f Food
	res := s.db.WithContext(ctx).Where("user_id = ? AND source = ? AND external_id = ?", userID, source, externalID).Limit(1).Find(&f)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, nil
	}
	return &f, nil
}

// FindWaterFood returns the user's Water food, or nil if they renamed it.
func (s *Store) FindWaterFood(ctx context.Context, userID string) (*Food, error) {
	var f Food
	res := s.db.WithContext(ctx).Where("user_id = ? AND name = ? AND source = ?", userID, WaterFoodName, SourceLabel).Order("created_at").Limit(1).Find(&f)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, nil
	}
	return &f, nil
}

// UpdateRow changes the given columns on one of the user's rows of model and
// reloads it into model. Fields not in changes keep their value.
func (s *Store) UpdateRow(ctx context.Context, userID, id string, model any, changes map[string]any) error {
	if uuid.Validate(id) != nil {
		return ErrNotFound
	}
	db := s.db.WithContext(ctx)
	if len(changes) > 0 {
		changes["updated_at"] = time.Now()
		res := db.Model(model).Where("id = ? AND user_id = ?", id, userID).Updates(changes)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrNotFound
		}
	}
	res := db.Where("id = ? AND user_id = ?", id, userID).Limit(1).Find(model)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// FindRequest returns a logging request seen before, or nil.
func (s *Store) FindRequest(ctx context.Context, userID, kind, requestID string) (*RequestLog, error) {
	var r RequestLog
	res := s.db.WithContext(ctx).Where("user_id = ? AND kind = ? AND request_id = ?", userID, kind, requestID).Limit(1).Find(&r)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, nil
	}
	return &r, nil
}

// CreateFoodLogs saves logs and the request that made them in one
// transaction. A log pointing at another user's food rolls everything back.
// A request id seen before returns ErrDuplicate.
func (s *Store) CreateFoodLogs(ctx context.Context, userID, requestID, date string, logs []*FoodLog) ([]string, error) {
	ids := make([]string, len(logs))
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for i, l := range logs {
			var n int64
			if err := tx.Model(&Food{}).Where("id = ? AND user_id = ?", l.FoodID, userID).Count(&n).Error; err != nil {
				return err
			}
			if n == 0 {
				return ErrNotFound
			}
			l.ID, l.UserID = newID(), userID
			ids[i] = l.ID
			if err := tx.Omit(clause.Associations).Create(l).Error; err != nil {
				return err
			}
		}
		return tx.Create(&RequestLog{UserID: userID, Kind: RequestFood, RequestID: requestID, Date: date, IDs: ids}).Error
	})
	if isUnique(err) {
		return nil, ErrDuplicate
	}
	return ids, err
}

// GetFoodLog returns one active log.
func (s *Store) GetFoodLog(ctx context.Context, userID, logID string) (*FoodLog, error) {
	if uuid.Validate(logID) != nil {
		return nil, ErrNotFound
	}
	var l FoodLog
	res := s.db.WithContext(ctx).Preload("Food").Where("id = ? AND user_id = ? AND archived_at IS NULL", logID, userID).Limit(1).Find(&l)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrNotFound
	}
	return &l, nil
}

// Archive hides one of the user's active rows of model. It reports whether
// anything changed.
func (s *Store) Archive(ctx context.Context, userID, id string, model any) (bool, error) {
	now := time.Now()
	res := s.db.WithContext(ctx).Model(model).Where("id = ? AND user_id = ? AND archived_at IS NULL", id, userID).
		Updates(map[string]any{"archived_at": now, "updated_at": now})
	return res.RowsAffected > 0, res.Error
}

// DayFoodLogs returns a day's active logs with their foods, in eaten order.
func (s *Store) DayFoodLogs(ctx context.Context, userID, date string) ([]*FoodLog, error) {
	var rows []*FoodLog
	err := s.db.WithContext(ctx).Preload("Food").Where("user_id = ? AND date = ? AND archived_at IS NULL", userID, date).
		Order("eaten_at, created_at").Find(&rows).Error
	return rows, err
}

// GetTargets returns the user's targets, or nil if none were ever set.
func (s *Store) GetTargets(ctx context.Context, userID string) (*Target, error) {
	var t Target
	res := s.db.WithContext(ctx).Where("user_id = ?", userID).Limit(1).Find(&t)
	if res.Error != nil || res.RowsAffected == 0 {
		return nil, res.Error
	}
	return &t, nil
}

// SaveTargets writes the user's whole set of targets.
func (s *Store) SaveTargets(ctx context.Context, userID string, t *Target) error {
	t.UserID = userID
	return s.db.WithContext(ctx).Save(t).Error
}

// CreateWorkout saves a workout and the request that made it.
func (s *Store) CreateWorkout(ctx context.Context, userID, requestID string, w *Workout) error {
	w.ID, w.UserID = newID(), userID
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(w).Error; err != nil {
			return err
		}
		return tx.Create(&RequestLog{UserID: userID, Kind: RequestWorkout, RequestID: requestID, Date: w.Date, IDs: []string{w.ID}}).Error
	})
	if isUnique(err) {
		return ErrDuplicate
	}
	return err
}

// GetWorkout returns one active workout.
func (s *Store) GetWorkout(ctx context.Context, userID, workoutID string) (*Workout, error) {
	if uuid.Validate(workoutID) != nil {
		return nil, ErrNotFound
	}
	var w Workout
	res := s.db.WithContext(ctx).Where("id = ? AND user_id = ? AND archived_at IS NULL", workoutID, userID).Limit(1).Find(&w)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrNotFound
	}
	return &w, nil
}

// DayWorkouts returns a day's active workouts.
func (s *Store) DayWorkouts(ctx context.Context, userID, date string) ([]*Workout, error) {
	var rows []*Workout
	err := s.db.WithContext(ctx).Where("user_id = ? AND date = ? AND archived_at IS NULL", userID, date).Order("created_at").Find(&rows).Error
	return rows, err
}
