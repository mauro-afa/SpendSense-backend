package service

import (
	"context"
	"fmt"
	"log"
	"math"
	"math/big"
	"strconv"
	"time"

	"github.com/BeWellSpent/wellspent-backend/internal/apperr"
	"github.com/BeWellSpent/wellspent-backend/internal/category"
	"github.com/BeWellSpent/wellspent-backend/internal/repository"
	db "github.com/BeWellSpent/wellspent-backend/internal/sqlc"
	"github.com/BeWellSpent/wellspent-backend/internal/tax"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

type BudgetProfileService struct {
	profiles      repository.BudgetProfileRepository
	transactions  repository.TransactionRepository
	fixedExpenses repository.FixedExpenseRepository
	users         repository.UserRepository
	notifs        *NotificationService
}

func NewBudgetProfileService(
	profiles repository.BudgetProfileRepository,
	transactions repository.TransactionRepository,
	fixedExpenses repository.FixedExpenseRepository,
	users repository.UserRepository,
) *BudgetProfileService {
	if profiles == nil {
		panic("NewBudgetProfileService: profiles is required")
	}
	if transactions == nil {
		panic("NewBudgetProfileService: transactions is required")
	}
	if fixedExpenses == nil {
		panic("NewBudgetProfileService: fixedExpenses is required")
	}
	if users == nil {
		panic("NewBudgetProfileService: users is required")
	}
	return &BudgetProfileService{profiles: profiles, transactions: transactions, fixedExpenses: fixedExpenses, users: users}
}

func (s *BudgetProfileService) WithNotifications(ns *NotificationService) *BudgetProfileService {
	s.notifs = ns
	return s
}

// ── Access helpers ────────────────────────────────────────────────────────────

// getUserRole returns the profile and the caller's effective role.
// Profile owners are always admin regardless of the role column (handles legacy data and the
// creation flow where the person row may not yet exist).
func (s *BudgetProfileService) getUserRole(ctx context.Context, profileID, userID uuid.UUID) (db.BudgetProfile, string, error) {
	profile, err := s.profiles.GetByID(ctx, profileID)
	if err != nil {
		return db.BudgetProfile{}, "", err
	}
	if profile.UserID == userID {
		return profile, "admin", nil
	}
	person, err := s.profiles.GetPersonByUserID(ctx, profileID, userID)
	if err != nil {
		return db.BudgetProfile{}, "", apperr.Forbidden("access denied")
	}
	return profile, person.Role, nil
}

func (s *BudgetProfileService) assertAdmin(ctx context.Context, profileID, userID uuid.UUID) (db.BudgetProfile, error) {
	profile, role, err := s.getUserRole(ctx, profileID, userID)
	if err != nil {
		return db.BudgetProfile{}, err
	}
	if role != "admin" {
		return db.BudgetProfile{}, apperr.Forbidden("access denied")
	}
	return profile, nil
}

func (s *BudgetProfileService) assertCollaboratorOrAbove(ctx context.Context, profileID, userID uuid.UUID) (db.BudgetProfile, error) {
	profile, role, err := s.getUserRole(ctx, profileID, userID)
	if err != nil {
		return db.BudgetProfile{}, err
	}
	if role != "admin" && role != "collaborator" {
		return db.BudgetProfile{}, apperr.Forbidden("access denied")
	}
	return profile, nil
}

func (s *BudgetProfileService) assertMember(ctx context.Context, profileID, userID uuid.UUID) (db.BudgetProfile, error) {
	profile, _, err := s.getUserRole(ctx, profileID, userID)
	if err != nil {
		return db.BudgetProfile{}, err
	}
	return profile, nil
}

func (s *BudgetProfileService) assertPeriodMember(ctx context.Context, periodID, userID uuid.UUID) (db.BudgetProfile, error) {
	period, err := s.profiles.GetPeriodByID(ctx, periodID)
	if err != nil {
		return db.BudgetProfile{}, err
	}
	return s.assertMember(ctx, period.BudgetProfileID, userID)
}

func (s *BudgetProfileService) assertPeriodCollaborator(ctx context.Context, periodID, userID uuid.UUID) (db.BudgetProfile, error) {
	period, err := s.profiles.GetPeriodByID(ctx, periodID)
	if err != nil {
		return db.BudgetProfile{}, err
	}
	if period.IsArchived {
		return db.BudgetProfile{}, apperr.Forbidden("this budget period is archived and read-only")
	}
	return s.assertCollaboratorOrAbove(ctx, period.BudgetProfileID, userID)
}

// ── Profile CRUD ──────────────────────────────────────────────────────────────

func (s *BudgetProfileService) List(ctx context.Context, userID uuid.UUID) ([]db.BudgetProfile, error) {
	return s.profiles.ListByUserOrMember(ctx, userID)
}

func (s *BudgetProfileService) Get(ctx context.Context, id, userID uuid.UUID) (db.BudgetProfile, error) {
	return s.assertMember(ctx, id, userID)
}

func (s *BudgetProfileService) Create(ctx context.Context, userID uuid.UUID, name, cycle string) (db.BudgetProfile, db.BudgetPeriod, error) {
	// Only one owned budget profile per user, regardless of plan tier — a
	// user can still be a *member* of other people's shared budgets without
	// limit; this only caps how many they can own.
	owned, err := s.profiles.ListByUserID(ctx, userID)
	if err != nil {
		return db.BudgetProfile{}, db.BudgetPeriod{}, fmt.Errorf("budget_profile: check owned: %w", err)
	}
	if len(owned) > 0 {
		return db.BudgetProfile{}, db.BudgetPeriod{}, apperr.Invalid("you already have a budget — only one budget profile is allowed per account")
	}

	exists, err := s.profiles.ExistsByNameAndUser(ctx, name, userID)
	if err != nil {
		return db.BudgetProfile{}, db.BudgetPeriod{}, fmt.Errorf("budget_profile: check exists: %w", err)
	}
	if exists {
		return db.BudgetProfile{}, db.BudgetPeriod{}, apperr.Duplicate("budget_profile", "name", name)
	}

	createParams := db.CreateBudgetProfileParams{
		UserID: userID,
		Name:   name,
		Cycle:  cycle,
	}
	// Propagate owner's country to the profile so tax features are country-gated.
	if owner, ownerErr := s.users.GetByID(ctx, userID); ownerErr == nil {
		createParams.CountryCode = owner.CountryCode
	}
	profile, err := s.profiles.Create(ctx, createParams)
	if err != nil {
		return db.BudgetProfile{}, db.BudgetPeriod{}, err
	}

	// Auto-add budget owner as the first person on the profile.
	owner, err := s.users.GetByID(ctx, userID)
	if err == nil {
		displayName := userDisplayName(owner)
		if _, personErr := s.profiles.AddPerson(ctx, db.AddBudgetPersonToProfileParams{
			BudgetProfileID: profile.ID,
			UserName:        &displayName,
			UserID:          &userID,
			Color:           "",
			Role:            "admin",
		}); personErr != nil {
			// The owner isn't on their own budget: no payment methods can be
			// attributed and the people list opens empty.
			log.Printf("budget_profile.create: add owner %s as first person on %s: %v", userID, profile.ID, personErr)
		}
	}

	// Create the first period immediately.
	period, err := s.createNextPeriod(ctx, profile)
	if err != nil {
		// Non-fatal: profile was created, period creation failed.
		return profile, db.BudgetPeriod{}, nil
	}
	return profile, period, nil
}

func (s *BudgetProfileService) Update(ctx context.Context, id, userID uuid.UUID, name, cycle string) (db.BudgetProfile, error) {
	if _, err := s.assertAdmin(ctx, id, userID); err != nil {
		return db.BudgetProfile{}, err
	}
	return s.profiles.Update(ctx, db.UpdateBudgetProfileParams{ID: id, Name: name, Cycle: cycle})
}

// SetCarryoverEnabled turns period carryover on or off for a budget. Admin
// only: it changes what every member's next period will contain.
func (s *BudgetProfileService) SetCarryoverEnabled(ctx context.Context, id, userID uuid.UUID, enabled bool) (db.BudgetProfile, error) {
	if _, err := s.assertAdmin(ctx, id, userID); err != nil {
		return db.BudgetProfile{}, err
	}
	return s.profiles.SetCarryoverEnabled(ctx, db.SetBudgetProfileCarryoverEnabledParams{
		ID:               id,
		CarryoverEnabled: enabled,
	})
}

// SetAutoUpdatePlannedAmount controls whether marking a fixed expense paid at a
// different amount rewrites its template. Admin only: it changes what every
// member's future periods are planned at.
func (s *BudgetProfileService) SetAutoUpdatePlannedAmount(ctx context.Context, id, userID uuid.UUID, enabled bool) (db.BudgetProfile, error) {
	if _, err := s.assertAdmin(ctx, id, userID); err != nil {
		return db.BudgetProfile{}, err
	}
	return s.profiles.SetAutoUpdatePlannedAmount(ctx, db.SetBudgetProfileAutoUpdatePlannedAmountParams{
		ID:                      id,
		AutoUpdatePlannedAmount: enabled,
	})
}

// Delete removes a budget profile and everything hanging off it.
//
// Two statements rather than one, in this order, because payment methods are
// the one thing reachable from a budget that does not carry a
// budget_profile_id: they are user-scoped and belong to the budget only
// through budget_person_id. So they are read *before* the delete (that link is
// the only way to find them, and it is about to be cut) and removed *after*
// it (transaction.payment_method_id and savings_source.payment_method_id have
// no ON DELETE, so those rows have to go with the profile first).
//
// Migration 000057 made the person foreign key ON DELETE SET NULL, which is
// what stops the profile delete failing outright; this cleanup is what stops
// it leaving rows behind that no query can ever reach again.
func (s *BudgetProfileService) Delete(ctx context.Context, id, userID uuid.UUID) error {
	if _, err := s.assertAdmin(ctx, id, userID); err != nil {
		return err
	}

	// Best-effort: a failure here must not block the delete the user asked
	// for. The worst case is an unreachable row, not a budget that refuses
	// to go away.
	paymentMethodIDs, listErr := s.transactions.ListPaymentMethodIDsByBudgetProfile(ctx, id)

	if err := s.profiles.Delete(ctx, id); err != nil {
		return err
	}

	if listErr == nil && len(paymentMethodIDs) > 0 {
		_ = s.transactions.DeletePaymentMethodsByIDs(ctx, paymentMethodIDs)
	}
	return nil
}

// ── Period ────────────────────────────────────────────────────────────────────

// CreateBudgetPeriod creates the next cycle window for a profile. Dates are computed
// from the profile's cycle and the previous period's end date (today for the first).
// Recurring income sources are pre-filled as entries; fixed+recurring transactions
// are carried forward from the previous period.
func (s *BudgetProfileService) CreateBudgetPeriod(ctx context.Context, profileID, userID uuid.UUID) (db.BudgetPeriod, error) {
	profile, err := s.assertAdmin(ctx, profileID, userID)
	if err != nil {
		return db.BudgetPeriod{}, err
	}
	return s.createNextPeriod(ctx, profile)
}

func (s *BudgetProfileService) createNextPeriod(ctx context.Context, profile db.BudgetProfile) (db.BudgetPeriod, error) {
	var startDate, endDate time.Time

	latest, err := s.profiles.GetLatestPeriod(ctx, profile.ID)
	if err != nil {
		// No previous period — use today as the start.
		startDate, endDate = computeFirstPeriodDates(profile.Cycle)
	} else {
		today := time.Now().UTC().Truncate(24 * time.Hour)
		// Idempotency: latest period is still active, nothing to create.
		if !latest.EndDate.Time.Before(today) {
			return latest, nil
		}
		startDate, endDate = computeNextPeriodDates(profile.Cycle, latest.EndDate.Time)
	}

	period, err := s.profiles.CreatePeriod(ctx, db.CreateBudgetPeriodParams{
		BudgetProfileID: profile.ID,
		StartDate:       pgtype.Date{Time: startDate, Valid: true},
		EndDate:         pgtype.Date{Time: endDate, Valid: true},
	})
	if err != nil {
		return db.BudgetPeriod{}, err
	}

	// Archive the period that just ended now that the new one is live.
	if latest.ID != (uuid.UUID{}) {
		if archiveErr := s.profiles.ArchivePeriod(ctx, latest.ID); archiveErr != nil {
			// Two live periods at once: both clients pick a "current" period and
			// would disagree about which.
			log.Printf("period_rollover: archive period %s after creating %s: %v", latest.ID, period.ID, archiveErr)
		}
	}

	// Pre-fill recurring income sources as entries.
	sources, _ := s.profiles.ListIncomeSources(ctx, profile.ID)
	for _, src := range sources {
		if !src.Recurring {
			continue
		}
		srcID := src.ID
		if _, incomeErr := s.profiles.CreateIncomeEntry(ctx, db.CreateIncomeEntryParams{
			BudgetPeriodID: period.ID,
			IncomeSourceID: &srcID,
			BudgetPersonID: src.BudgetPersonID,
			Name:           &src.Name,
			Amount:         src.DefaultAmount,
		}); incomeErr != nil {
			// The period opens with less income than the user expects, which
			// silently inflates every "remaining to allocate" figure.
			log.Printf("period_rollover: pre-fill income source %d into period %s: %v", srcID, period.ID, incomeErr)
		}
	}

	// Recalculate per-person tax reserve entries.
	s.recalculateTaxReserve(ctx, profile.ID)

	// Recreate savings transactions for the new period.
	savingsSrcs, _ := s.profiles.ListSavingsSources(ctx, profile.ID)
	for _, src := range savingsSrcs {
		if src.PaymentMethodID == nil || len(src.PaymentDays) == 0 {
			continue
		}
		s.createSavingsTransactions(ctx, profile.ID, profile.UserID, src)
	}

	// Spawn fixed expense transactions for the new period — only for expenses
	// actually due this period's month (see isFixedExpenseDueInMonth), and at
	// most once per calendar month even if weekly/bi-weekly cycles land more
	// than one period inside that month. WEEK-unit expenses use a separate
	// per-date path since a single period can contain several due weeks.
	fixedExpenses, _ := s.fixedExpenses.List(ctx, profile.ID)
	activeMethods := s.activePaymentMethodIDs(ctx, profile.ID)
	txTypeFixed := int32(1)
	monthStart := time.Date(startDate.Year(), startDate.Month(), 1, 0, 0, 0, 0, time.UTC)
	monthEnd := monthStart.AddDate(0, 1, 0)
	for _, fe := range fixedExpenses {
		// Auto-deactivate when the plan's end date has passed.
		if fe.EndDate.Valid && startDate.After(fe.EndDate.Time) {
			if deactivateErr := s.fixedExpenses.Deactivate(ctx, db.DeactivateFixedExpenseParams{
				ID:              fe.ID,
				BudgetProfileID: profile.ID,
			}); deactivateErr != nil {
				// A finished payment plan keeps spawning bills the user no longer owes.
				log.Printf("period_rollover: deactivate ended fixed expense %s: %v", fe.ID, deactivateErr)
			}
			continue
		}
		if isFixedExpenseWeekUnit(fe) {
			s.spawnWeeklyFixedExpenseOccurrences(ctx, fe, period.ID, startDate, endDate, activeMethods)
			continue
		}
		if !isFixedExpenseDueInMonth(fe, monthStart) {
			continue
		}
		feID := fe.ID
		exists, _ := s.fixedExpenses.HasTransactionInMonth(ctx, db.FixedExpenseHasTransactionInMonthParams{
			FixedExpenseID: feID,
			MonthStart:     pgtype.Date{Time: monthStart, Valid: true},
			MonthEnd:       pgtype.Date{Time: monthEnd, Valid: true},
		})
		if exists {
			continue
		}
		txDate := pgtype.Date{Time: fixedExpenseDateInMonth(fe, monthStart), Valid: true}
		name := fe.Name
		if _, spawnErr := s.transactions.Create(ctx, db.CreateTransactionParams{
			Name:              &name,
			Amount:            fe.PlannedAmount,
			PlannedAmount:     fe.PlannedAmount,
			Date:              txDate,
			BudgetPeriodID:    &period.ID,
			CategoryID:        fe.CategoryID,
			PaymentMethodID:   livePaymentMethod(fe.PaymentMethodID, activeMethods),
			TransactionTypeID: &txTypeFixed,
			FixedExpenseID:    &feID,
		}); spawnErr != nil {
			// A bill the user owes this period simply never appears.
			log.Printf("period_rollover: spawn fixed expense %s into period %s: %v", feID, period.ID, spawnErr)
		}
	}

	// Carry the closing period's ending balance forward, if the budget opted
	// in. Runs last: it reads only the period that just closed, so nothing
	// above depends on it, and a failure here must not cost the user their
	// new period.
	if latest.ID != (uuid.UUID{}) {
		s.applyCarryover(ctx, profile, latest, period)
	}

	if s.notifs != nil {
		s.notifs.HandlePeriodCreated(ctx, period)
	}

	return period, nil
}

// applyCarryover turns the closing period's ending balance into transactions in
// the new one — see carryover.go for the rule.
//
// Silent on every failure, matching the rest of createNextPeriod: a budget
// period that exists without its carryover is recoverable (the user can record
// the balance by hand), a period that failed to be created is not.
func (s *BudgetProfileService) applyCarryover(ctx context.Context, profile db.BudgetProfile, closing, next db.BudgetPeriod) {
	if !profile.CarryoverEnabled {
		return
	}

	// Idempotency. The daily cycling job and the client-callable
	// CreateBudgetPeriod RPC both reach here, and there is no transaction
	// wrapping this function, so "did I already carry this period?" has to be
	// a real question asked of the database rather than an assumption.
	carried, err := s.transactions.CountCarried(ctx, db.CountCarriedTransactionsParams{
		BudgetPeriodID:            next.ID,
		CarriedFromBudgetPeriodID: closing.ID,
	})
	if err != nil {
		log.Printf("carryover: check whether period %s was already carried into %s: %v", closing.ID, next.ID, err)
		return
	}
	if carried > 0 {
		return
	}

	categoryIDs, err := s.transactions.ListSystemCategories(ctx)
	if err != nil {
		log.Printf("carryover: load system categories for profile %s: %v", profile.ID, err)
		return
	}
	nonSpend := nonSpendCategoryIDs(categoryIDs)

	txs, err := s.transactions.List(ctx, db.ListTransactionsParams{BudgetPeriodID: closing.ID})
	if err != nil {
		log.Printf("carryover: list transactions for closing period %s: %v", closing.ID, err)
		return
	}
	incomeEntries, err := s.profiles.ListIncomeEntries(ctx, closing.ID)
	if err != nil {
		log.Printf("carryover: list income entries for closing period %s: %v", closing.ID, err)
		return
	}

	remainder, spendByMethod, unattributed := carryoverInputs(txs, incomeEntries, nonSpend)
	rows := computeCarryover(remainder, spendByMethod, unattributed)
	if len(rows) == 0 {
		return
	}

	txTypeVariable := int32(2)
	txDate := pgtype.Date{Time: next.StartDate.Time, Valid: true}
	closingID := closing.ID
	for _, row := range rows {
		categoryID, ok := categoryIDs[row.categoryKey]
		if !ok {
			// Savings and Debt are both seeded system categories, so this only
			// fires on a database missing migration 000052. Skipping beats
			// creating an uncategorized row the user can't interpret.
			log.Printf("carryover: system category %q missing — skipping a carried row for profile %s", row.categoryKey, profile.ID)
			continue
		}
		name := carryoverTransactionName(row, closing)
		amount := numericFromNanos(row.amountNanos)
		if _, createErr := s.transactions.Create(ctx, db.CreateTransactionParams{
			Name:                      &name,
			Amount:                    amount,
			PlannedAmount:             amount,
			Date:                      txDate,
			BudgetPeriodID:            &next.ID,
			CategoryID:                &categoryID,
			PaymentMethodID:           row.paymentMethodID,
			TransactionTypeID:         &txTypeVariable,
			CarriedFromBudgetPeriodID: &closingID,
		}); createErr != nil {
			// Partial carryover: the rows already written stay, so the carried
			// total no longer matches the balance it came from.
			log.Printf("carryover: create %s row for period %s from %s: %v", row.categoryKey, next.ID, closing.ID, createErr)
		}
	}
}

// carryoverTransactionName labels a carried row with the period it came from,
// so the transaction reads as an explanation rather than a mystery charge.
// Clients additionally caption the row from carried_from_budget_period_id; this
// is what a bare CSV export or a psql session sees.
func carryoverTransactionName(row carryoverRow, closing db.BudgetPeriod) string {
	label := closing.StartDate.Time.Format("Jan 2006")
	if row.categoryKey == carryoverCategorySavings {
		return "Left over from " + label
	}
	return "Carried balance from " + label
}

// fixedExpenseMonthIndex converts a date to an absolute month number
// (year*12 + month) for interval arithmetic.
func fixedExpenseMonthIndex(t time.Time) int {
	return t.Year()*12 + int(t.Month())
}

// fixedExpenseAnchor returns the anchor time used for interval arithmetic:
// fe.AnchorDate when explicitly set (lets a fixed expense start in the
// future instead of at creation time), otherwise fe.CreatedAt.
func fixedExpenseAnchor(fe db.FixedExpense) time.Time {
	if fe.AnchorDate.Valid {
		return fe.AnchorDate.Time
	}
	return fe.CreatedAt.Time
}

// isFixedExpenseDueInMonth reports whether fe is due in the month starting at
// monthStart, using fixedExpenseAnchor's month as the anchor: due when the
// number of months elapsed since the anchor is a multiple of IntervalMonths.
// A monthStart before the anchor month is never due (covers a future-dated
// AnchorDate not having arrived yet).
func isFixedExpenseDueInMonth(fe db.FixedExpense, monthStart time.Time) bool {
	interval := int(fe.IntervalMonths)
	if interval < 1 {
		interval = 1
	}
	diff := fixedExpenseMonthIndex(monthStart) - fixedExpenseMonthIndex(fixedExpenseAnchor(fe))
	if diff < 0 {
		return false
	}
	return diff%interval == 0
}

// fixedExpenseDateInMonth returns fe's transaction date within monthStart's
// month, clamping DayOfMonth to that month's last day when needed.
func fixedExpenseDateInMonth(fe db.FixedExpense, monthStart time.Time) time.Time {
	lastDay := time.Date(monthStart.Year(), monthStart.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
	day := int(fe.DayOfMonth)
	if day < 1 {
		day = 1
	}
	if day > lastDay {
		day = lastDay
	}
	return time.Date(monthStart.Year(), monthStart.Month(), day, 0, 0, 0, 0, time.UTC)
}

// FixedExpenseNextDueDate returns the next date on or after `from` that fe is
// due, as a full transaction date (day-of-month/day-of-week applied and
// clamped as appropriate). Used to surface a computed, non-persisted
// "next due" field to clients.
func FixedExpenseNextDueDate(fe db.FixedExpense, from time.Time) time.Time {
	if isFixedExpenseWeekUnit(fe) {
		return fixedExpenseNextDueDateWeekly(fe, from)
	}
	interval := int(fe.IntervalMonths)
	if interval < 1 {
		interval = 1
	}
	monthStart := time.Date(from.Year(), from.Month(), 1, 0, 0, 0, 0, time.UTC)
	// A future anchor (AnchorDate ahead of `from`) can be more than `interval`
	// months away, which the interval-bounded search below wouldn't reach —
	// no due month can occur before the anchor's own month, so start there.
	anchor := fixedExpenseAnchor(fe)
	anchorMonthStart := time.Date(anchor.Year(), anchor.Month(), 1, 0, 0, 0, 0, time.UTC)
	if anchorMonthStart.After(monthStart) {
		monthStart = anchorMonthStart
	}
	for i := 0; i < interval; i++ {
		if isFixedExpenseDueInMonth(fe, monthStart) {
			return fixedExpenseDateInMonth(fe, monthStart)
		}
		monthStart = monthStart.AddDate(0, 1, 0)
	}
	// Unreachable in practice (a multiple of interval is always found within
	// `interval` months), but fall back to `from`'s month if it somehow isn't.
	return fixedExpenseDateInMonth(fe, time.Date(from.Year(), from.Month(), 1, 0, 0, 0, 0, time.UTC))
}

// ── Weekly cadence (frequency_unit = WEEK) ──────────────────────────────────
//
// Parallel to the month-index math above, but weeks are counted linearly from
// a fixed Monday reference point rather than ISO calendar week-of-year, to
// avoid year-boundary rollover edge cases (week 52/53 -> week 1).

const (
	frequencyUnitMonth int16 = 1 // matches FrequencyUnit_FREQUENCY_UNIT_MONTH (also the default for UNSPECIFIED/0)
	frequencyUnitWeek  int16 = 2 // matches FrequencyUnit_FREQUENCY_UNIT_WEEK
)

// mondayEpoch is a stable Monday reference point (Jan 5 1970) for linear
// week-index arithmetic.
var mondayEpoch = time.Date(1970, 1, 5, 0, 0, 0, 0, time.UTC)

func isFixedExpenseWeekUnit(fe db.FixedExpense) bool {
	return fe.FrequencyUnit == frequencyUnitWeek
}

// fixedExpenseWeekIndex converts a date to a linear week number since
// mondayEpoch, for interval arithmetic parallel to fixedExpenseMonthIndex.
func fixedExpenseWeekIndex(t time.Time) int {
	days := int(t.Truncate(24*time.Hour).Sub(mondayEpoch).Hours() / 24)
	return int(math.Floor(float64(days) / 7))
}

// fixedExpenseWeekStart returns the Monday of t's week.
func fixedExpenseWeekStart(t time.Time) time.Time {
	t = t.Truncate(24 * time.Hour)
	offset := (int(t.Weekday()) + 6) % 7 // days since Monday (time.Weekday: Sunday=0..Saturday=6)
	return t.AddDate(0, 0, -offset)
}

// isFixedExpenseDueInWeek reports whether fe is due in the week starting at
// ws (a Monday), mirroring isFixedExpenseDueInMonth.
func isFixedExpenseDueInWeek(fe db.FixedExpense, ws time.Time) bool {
	interval := int(fe.IntervalWeeks)
	if interval < 1 {
		interval = 1
	}
	diff := fixedExpenseWeekIndex(ws) - fixedExpenseWeekIndex(fixedExpenseWeekStart(fixedExpenseAnchor(fe)))
	if diff < 0 {
		return false
	}
	return diff%interval == 0
}

// fixedExpenseDateInWeek returns fe's transaction date within the week
// starting at ws, applying DayOfWeek (1=Monday..7=Sunday). Every week has
// all 7 days, so unlike day-of-month there's no clamping needed.
func fixedExpenseDateInWeek(fe db.FixedExpense, ws time.Time) time.Time {
	dow := int(fe.DayOfWeek)
	if dow < 1 || dow > 7 {
		dow = 1
	}
	return ws.AddDate(0, 0, dow-1)
}

// fixedExpenseNextDueDateWeekly is FixedExpenseNextDueDate's WEEK-unit path.
func fixedExpenseNextDueDateWeekly(fe db.FixedExpense, from time.Time) time.Time {
	interval := int(fe.IntervalWeeks)
	if interval < 1 {
		interval = 1
	}
	ws := fixedExpenseWeekStart(from)
	anchorWeekStart := fixedExpenseWeekStart(fixedExpenseAnchor(fe))
	if anchorWeekStart.After(ws) {
		ws = anchorWeekStart
	}
	for i := 0; i < interval; i++ {
		if isFixedExpenseDueInWeek(fe, ws) {
			return fixedExpenseDateInWeek(fe, ws)
		}
		ws = ws.AddDate(0, 0, 7)
	}
	// Unreachable in practice, same rationale as the monthly fallback above.
	return fixedExpenseDateInWeek(fe, fixedExpenseWeekStart(from))
}

// FixedExpensePaymentsMade returns how many of fe's planned payments have come
// due as of `asOf`, clamped to [0, TotalPayments]. Returns 0 when TotalPayments
// is unset — "3 of 0 payments" is not a progress figure.
//
// Computed rather than persisted, exactly like FixedExpenseNextDueDate: the
// answer depends on today's date, so storing it would be stale the next day.
//
// This lives on the server because a client cannot get it right. The schedule
// anchors on fixedExpenseAnchor, which falls back to fe.CreatedAt when
// AnchorDate is null (every row predating migration 000025) — and CreatedAt is
// not on the wire. Both clients substituted an anchor of their own and
// disagreed with each other and, on web, with itself.
//
// It also counts actual due dates rather than whole calendar months. The
// previous client formulas floored a month difference and added one, so a bill
// due on the 28th counted February's payment as already made on February 3rd.
func FixedExpensePaymentsMade(fe db.FixedExpense, asOf time.Time) int32 {
	if fe.TotalPayments == nil || *fe.TotalPayments <= 0 {
		return 0
	}
	var made int
	if isFixedExpenseWeekUnit(fe) {
		made = fixedExpensePaymentsMadeWeekly(fe, asOf)
	} else {
		made = fixedExpensePaymentsMadeMonthly(fe, asOf)
	}
	if made < 0 {
		made = 0
	}
	if total := int(*fe.TotalPayments); made > total {
		made = total
	}
	return int32(made)
}

// fixedExpensePaymentsMadeMonthly counts due occurrences at or before asOf for
// a MONTH-unit expense. Due months are the anchor month plus multiples of
// IntervalMonths, so the count is however many whole intervals have elapsed,
// plus one more if that interval's own due date has actually arrived.
func fixedExpensePaymentsMadeMonthly(fe db.FixedExpense, asOf time.Time) int {
	interval := int(fe.IntervalMonths)
	if interval < 1 {
		interval = 1
	}
	diff := fixedExpenseMonthIndex(asOf) - fixedExpenseMonthIndex(fixedExpenseAnchor(fe))
	if diff < 0 {
		return 0
	}
	elapsed := diff / interval
	if diff%interval != 0 {
		// The most recent due month is behind us, so its payment is in.
		return elapsed + 1
	}
	// asOf falls inside a due month: count it only once its day has arrived.
	monthStart := time.Date(asOf.Year(), asOf.Month(), 1, 0, 0, 0, 0, time.UTC)
	if fixedExpenseDateInMonth(fe, monthStart).After(asOf) {
		return elapsed
	}
	return elapsed + 1
}

// fixedExpensePaymentsMadeWeekly is the WEEK-unit counterpart, over week
// indices instead of month indices.
func fixedExpensePaymentsMadeWeekly(fe db.FixedExpense, asOf time.Time) int {
	interval := int(fe.IntervalWeeks)
	if interval < 1 {
		interval = 1
	}
	ws := fixedExpenseWeekStart(asOf)
	diff := fixedExpenseWeekIndex(ws) - fixedExpenseWeekIndex(fixedExpenseWeekStart(fixedExpenseAnchor(fe)))
	if diff < 0 {
		return 0
	}
	elapsed := diff / interval
	if diff%interval != 0 {
		return elapsed + 1
	}
	if fixedExpenseDateInWeek(fe, ws).After(asOf) {
		return elapsed
	}
	return elapsed + 1
}

// spawnWeeklyFixedExpenseOccurrences spawns one transaction for every due
// week (per IntervalWeeks/DayOfWeek) whose date falls within
// [startDate, endDate). Unlike MONTH-unit expenses — at most one transaction
// per period — a WEEK-unit expense can have several due occurrences inside a
// single period (e.g. "every week" within a monthly budget period), so
// de-duplication is per exact date rather than per calendar month.
func (s *BudgetProfileService) spawnWeeklyFixedExpenseOccurrences(ctx context.Context, fe db.FixedExpense, periodID uuid.UUID, startDate, endDate time.Time, activeMethods map[uuid.UUID]bool) {
	txTypeFixed := int32(1)
	feID := fe.ID
	name := fe.Name
	for ws := fixedExpenseWeekStart(startDate); ws.Before(endDate); ws = ws.AddDate(0, 0, 7) {
		if !isFixedExpenseDueInWeek(fe, ws) {
			continue
		}
		date := fixedExpenseDateInWeek(fe, ws)
		if date.Before(startDate) || !date.Before(endDate) {
			continue
		}
		exists, _ := s.fixedExpenses.HasTransactionOnDate(ctx, db.FixedExpenseHasTransactionOnDateParams{
			FixedExpenseID: feID,
			TargetDate:     pgtype.Date{Time: date, Valid: true},
		})
		if exists {
			continue
		}
		if _, savErr := s.transactions.Create(ctx, db.CreateTransactionParams{
			Name:              &name,
			Amount:            fe.PlannedAmount,
			PlannedAmount:     fe.PlannedAmount,
			Date:              pgtype.Date{Time: date, Valid: true},
			BudgetPeriodID:    &periodID,
			CategoryID:        fe.CategoryID,
			PaymentMethodID:   livePaymentMethod(fe.PaymentMethodID, activeMethods),
			TransactionTypeID: &txTypeFixed,
			FixedExpenseID:    &feID,
		}); savErr != nil {
			// A weekly bill the user owes this period simply never appears.
			log.Printf("period_rollover: spawn weekly occurrence of fixed expense %s into period %s: %v", feID, periodID, savErr)
		}
	}
}

func (s *BudgetProfileService) ListBudgetPeriods(ctx context.Context, profileID, userID uuid.UUID) ([]db.BudgetPeriod, error) {
	if _, err := s.assertMember(ctx, profileID, userID); err != nil {
		return nil, err
	}
	return s.profiles.ListPeriods(ctx, profileID)
}

func (s *BudgetProfileService) GetBudgetPeriod(ctx context.Context, periodID, userID uuid.UUID) (db.BudgetPeriod, error) {
	period, err := s.profiles.GetPeriodByID(ctx, periodID)
	if err != nil {
		return db.BudgetPeriod{}, err
	}
	if _, err := s.assertMember(ctx, period.BudgetProfileID, userID); err != nil {
		return db.BudgetPeriod{}, err
	}
	return period, nil
}

// ── People ────────────────────────────────────────────────────────────────────

type ProfilePersonInput struct {
	UserName string
	UserID   *uuid.UUID
	Color    string
}

func (s *BudgetProfileService) AddPeople(ctx context.Context, profileID, userID uuid.UUID, people []ProfilePersonInput) ([]db.BudgetToProfileMapping, error) {
	profile, err := s.assertAdmin(ctx, profileID, userID)
	if err != nil {
		return nil, err
	}
	// Free tier: max 2 active people per budget.
	if owner, ownerErr := s.users.GetByID(ctx, profile.UserID); ownerErr == nil && owner.Plan == "free" {
		existing, listErr := s.profiles.ListPeople(ctx, profileID)
		if listErr == nil {
			active := 0
			for _, p := range existing {
				if p.IsActive {
					active++
				}
			}
			if active+len(people) > 2 {
				return nil, apperr.Invalid("free tier: budget is limited to 2 people; upgrade to Pro for unlimited members")
			}
		}
	}
	var results []db.BudgetToProfileMapping
	for _, p := range people {
		// Country constraint: if the person being added is a registered user,
		// they must be in the same country as the budget profile.
		if p.UserID != nil {
			person, personErr := s.users.GetByID(ctx, *p.UserID)
			if personErr == nil && profile.CountryCode != nil && person.CountryCode != nil &&
				*person.CountryCode != *profile.CountryCode {
				return nil, apperr.Invalid("all budget members must be in the same country")
			}
		}
		exists, err := s.profiles.ExistsPerson(ctx, profileID, p.UserName)
		if err != nil {
			return nil, err
		}
		if exists {
			return nil, apperr.Duplicate("person", "name", p.UserName)
		}
		role := "unspecified"
		if p.UserID != nil {
			role = "collaborator"
		}
		m, err := s.profiles.AddPerson(ctx, db.AddBudgetPersonToProfileParams{
			BudgetProfileID: profileID,
			UserName:        &p.UserName,
			UserID:          p.UserID,
			Color:           p.Color,
			Role:            role,
		})
		if err != nil {
			return nil, err
		}
		results = append(results, m)
	}
	return results, nil
}

func (s *BudgetProfileService) UpdatePerson(ctx context.Context, profileID uuid.UUID, personID int32, color string, userID uuid.UUID) (db.BudgetToProfileMapping, error) {
	if _, err := s.assertAdmin(ctx, profileID, userID); err != nil {
		return db.BudgetToProfileMapping{}, err
	}
	return s.profiles.UpdatePerson(ctx, db.UpdateBudgetPersonParams{
		ID:              personID,
		BudgetProfileID: profileID,
		Color:           color,
	})
}

// UpdateMyPreferences writes the caller's own presentation settings. No role
// check: a Viewer may still choose how they look at the budget. The row is
// matched on userID in SQL, so this cannot reach another member — which is
// also why the RPC takes no person ID.
func (s *BudgetProfileService) UpdateMyPreferences(ctx context.Context, profileID uuid.UUID, planChart, overviewChart *string, userID uuid.UUID) (db.BudgetToProfileMapping, error) {
	if _, err := s.assertMember(ctx, profileID, userID); err != nil {
		return db.BudgetToProfileMapping{}, err
	}
	return s.profiles.UpdatePersonPreferences(ctx, db.UpdateBudgetPersonPreferencesParams{
		BudgetProfileID:   profileID,
		UserID:            userID,
		PlanChartType:     planChart,
		OverviewChartType: overviewChart,
	})
}

func (s *BudgetProfileService) UpdatePersonRole(ctx context.Context, profileID uuid.UUID, personID int32, role string, userID uuid.UUID) (db.BudgetToProfileMapping, error) {
	if _, err := s.assertAdmin(ctx, profileID, userID); err != nil {
		return db.BudgetToProfileMapping{}, err
	}
	return s.profiles.UpdatePersonRole(ctx, db.UpdateBudgetPersonRoleParams{
		ID:              personID,
		BudgetProfileID: profileID,
		Role:            role,
	})
}

func (s *BudgetProfileService) ListPeople(ctx context.Context, profileID, userID uuid.UUID) ([]db.BudgetToProfileMapping, error) {
	if _, err := s.assertMember(ctx, profileID, userID); err != nil {
		return nil, err
	}
	return s.profiles.ListPeople(ctx, profileID)
}

func (s *BudgetProfileService) RemovePerson(ctx context.Context, profileID uuid.UUID, personID int32, replacementPersonID int32, replacementPMID uuid.UUID, userID uuid.UUID) error {
	profile, err := s.assertAdmin(ctx, profileID, userID)
	if err != nil {
		return err
	}
	person, err := s.profiles.GetPerson(ctx, personID, profileID)
	if err != nil {
		return err
	}
	// Protect the profile owner from removal.
	if person.UserID != nil && *person.UserID == profile.UserID {
		return apperr.Invalid("budget owner cannot be removed")
	}
	if replacementPersonID == 0 {
		return s.profiles.SoftRemovePerson(ctx, db.SoftRemovePersonFromProfileParams{
			PersonID:        personID,
			BudgetProfileID: profileID,
		})
	}
	if _, err := s.profiles.GetPerson(ctx, replacementPersonID, profileID); err != nil {
		return apperr.NotFound("replacement_person", fmt.Sprintf("%d", replacementPersonID))
	}
	repID := replacementPersonID
	return s.profiles.SoftRemovePersonAndReassign(ctx, db.SoftRemovePersonAndReassignFromProfileParams{
		PersonID:            personID,
		BudgetProfileID:     profileID,
		ReplacementPmID:     replacementPMID,
		ReplacementPersonID: &repID,
	})
}

// ── Income Sources ────────────────────────────────────────────────────────────

type IncomeSourceInput struct {
	Name             string
	IncomeType       string
	DefaultAmount    pgtype.Numeric
	Recurring        bool
	BudgetPersonID   *int32
	PaymentFrequency string
	BeforeTax        bool
}

func (s *BudgetProfileService) AddIncomeSource(ctx context.Context, profileID, userID uuid.UUID, inp IncomeSourceInput) (db.IncomeSource, error) {
	profile, err := s.assertCollaboratorOrAbove(ctx, profileID, userID)
	if err != nil {
		return db.IncomeSource{}, err
	}
	// Free tier: max 2 income sources per person.
	if owner, ownerErr := s.users.GetByID(ctx, profile.UserID); ownerErr == nil && owner.Plan == "free" {
		existing, listErr := s.profiles.ListIncomeSources(ctx, profileID)
		if listErr == nil {
			count := 0
			for _, src := range existing {
				sameP := (inp.BudgetPersonID == nil && src.BudgetPersonID == nil) ||
					(inp.BudgetPersonID != nil && src.BudgetPersonID != nil && *inp.BudgetPersonID == *src.BudgetPersonID)
				if sameP {
					count++
				}
			}
			if count >= 2 {
				return db.IncomeSource{}, apperr.Invalid("free tier: income sources are limited to 2 per person; upgrade to Pro for unlimited")
			}
		}
	}
	src, err := s.profiles.AddIncomeSource(ctx, db.AddIncomeSourceParams{
		BudgetProfileID:  profileID,
		BudgetPersonID:   inp.BudgetPersonID,
		Name:             inp.Name,
		IncomeType:       inp.IncomeType,
		DefaultAmount:    inp.DefaultAmount,
		Recurring:        inp.Recurring,
		PaymentFrequency: inp.PaymentFrequency,
		BeforeTax:        inp.BeforeTax,
	})
	if err != nil {
		return db.IncomeSource{}, err
	}
	s.recalculateTaxReserve(ctx, profileID)
	return src, nil
}

func (s *BudgetProfileService) ListIncomeSources(ctx context.Context, profileID, userID uuid.UUID) ([]db.IncomeSource, error) {
	if _, err := s.assertMember(ctx, profileID, userID); err != nil {
		return nil, err
	}
	return s.profiles.ListIncomeSources(ctx, profileID)
}

func (s *BudgetProfileService) UpdateIncomeSource(ctx context.Context, id int32, profileID, userID uuid.UUID, inp IncomeSourceInput) (db.IncomeSource, error) {
	if _, err := s.assertCollaboratorOrAbove(ctx, profileID, userID); err != nil {
		return db.IncomeSource{}, err
	}
	src, err := s.profiles.UpdateIncomeSource(ctx, db.UpdateIncomeSourceParams{
		ID:               id,
		BudgetProfileID:  profileID,
		Name:             inp.Name,
		IncomeType:       inp.IncomeType,
		DefaultAmount:    inp.DefaultAmount,
		Recurring:        inp.Recurring,
		BudgetPersonID:   inp.BudgetPersonID,
		PaymentFrequency: inp.PaymentFrequency,
		BeforeTax:        inp.BeforeTax,
	})
	if err != nil {
		return db.IncomeSource{}, err
	}
	s.recalculateTaxReserve(ctx, profileID)
	return src, nil
}

func (s *BudgetProfileService) DeleteIncomeSource(ctx context.Context, id int32, profileID, userID uuid.UUID) error {
	if _, err := s.assertCollaboratorOrAbove(ctx, profileID, userID); err != nil {
		return err
	}
	if err := s.profiles.DeleteIncomeSource(ctx, db.DeleteIncomeSourceParams{
		ID:              id,
		BudgetProfileID: profileID,
	}); err != nil {
		return err
	}
	s.recalculateTaxReserve(ctx, profileID)
	return nil
}

// recalculateTaxReserve recomputes per-person tax reserve savings entries for a
// US profile whenever income sources change. It is best-effort — failures are
// silently ignored so the primary mutation is never blocked.
func (s *BudgetProfileService) recalculateTaxReserve(ctx context.Context, profileID uuid.UUID) {
	profile, err := s.profiles.GetByID(ctx, profileID)
	if err != nil {
		return
	}

	// Resolve effective country (fall back to owner when profile predates the column).
	countryCode := ""
	if profile.CountryCode != nil {
		countryCode = *profile.CountryCode
	}
	if countryCode == "" {
		if owner, oErr := s.users.GetByID(ctx, profile.UserID); oErr == nil && owner.CountryCode != nil {
			countryCode = *owner.CountryCode
		}
	}
	if countryCode != "US" {
		return
	}

	sources, err := s.profiles.ListIncomeSources(ctx, profileID)
	if err != nil {
		return
	}

	// Build people list once — used both to find the owner's person entry and
	// to map person → linked user for individual tax settings.
	people, _ := s.profiles.ListPeople(ctx, profileID)
	var ownerPersonID *int32
	userByPerson := map[int32]uuid.UUID{}
	for _, p := range people {
		if p.UserID != nil {
			userByPerson[p.ID] = *p.UserID
			if *p.UserID == profile.UserID {
				pid := p.ID
				ownerPersonID = &pid
			}
		}
	}

	// Group annual before-tax income by person ID.
	// Unattributed income (person 0) falls back to the owner's person entry.
	incomeByPerson := map[int32]float64{}
	for _, src := range sources {
		if !src.BeforeTax || !src.DefaultAmount.Valid {
			continue
		}
		f, _ := new(big.Float).SetInt(src.DefaultAmount.Int).Float64()
		if src.DefaultAmount.Exp > 0 {
			f *= math.Pow(10, float64(src.DefaultAmount.Exp))
		} else if src.DefaultAmount.Exp < 0 {
			f /= math.Pow(10, float64(-src.DefaultAmount.Exp))
		}
		f *= 12 // monthly → annual

		pid := int32(0)
		if src.BudgetPersonID != nil {
			pid = *src.BudgetPersonID
		}
		if pid == 0 && ownerPersonID != nil {
			pid = *ownerPersonID
		}
		incomeByPerson[pid] += f
	}

	// Delete all existing tax reserve entries; re-create one per person.
	if taxErr := s.profiles.DeleteTaxReserveSavingsSource(ctx, profileID); taxErr != nil {
		log.Printf("tax_reserve: clear existing reserve for profile %s: %v", profileID, taxErr)
	}
	if len(incomeByPerson) == 0 {
		return
	}

	// Load owner for fallback tax settings.
	owner, err := s.users.GetByID(ctx, profile.UserID)
	if err != nil {
		return
	}
	ownerState := ""
	if owner.StateCode != nil {
		ownerState = *owner.StateCode
	}
	ownerFS, _ := strconv.Atoi(owner.FilingStatus)

	toMonthly := func(annual float64) pgtype.Numeric {
		return pgtype.Numeric{Int: big.NewInt(int64(math.Round(annual / 12 * 100))), Exp: -2, Valid: true}
	}

	for personID, annualIncome := range incomeByPerson {
		if annualIncome == 0 {
			continue
		}
		pid := personID // local copy for pointer

		// Use the person's own tax settings if they are a linked user.
		stateCode, fsInt := ownerState, ownerFS
		if uid, ok := userByPerson[personID]; ok {
			if u, uErr := s.users.GetByID(ctx, uid); uErr == nil {
				if u.StateCode != nil {
					stateCode = *u.StateCode
				}
				if u.FilingStatus != "" {
					fsInt, _ = strconv.Atoi(u.FilingStatus)
				}
			}
		}

		estimate := tax.Estimate(annualIncome, stateCode, tax.FilingStatus(fsInt))
		if _, upsertErr := s.profiles.UpsertTaxReserveSavingsSource(ctx, db.UpsertTaxReserveSavingsSourceParams{
			BudgetProfileID: profileID,
			BudgetPersonID:  &pid,
			Amount:          toMonthly(estimate.TotalAnnual),
			FederalAmount:   toMonthly(estimate.FederalTax),
			StateAmount:     toMonthly(estimate.StateTax),
		}); upsertErr != nil {
			// The person's tax reserve silently stops tracking their income.
			log.Printf("tax_reserve: upsert reserve for profile %s: %v", profileID, upsertErr)
		}
	}
}

// ── Income Entries ────────────────────────────────────────────────────────────

func (s *BudgetProfileService) ListIncomeEntries(ctx context.Context, periodID, userID uuid.UUID) ([]db.IncomeEntry, error) {
	if _, err := s.assertPeriodMember(ctx, periodID, userID); err != nil {
		return nil, err
	}
	return s.profiles.ListIncomeEntries(ctx, periodID)
}

func (s *BudgetProfileService) UpdateIncomeEntry(ctx context.Context, id int32, periodID uuid.UUID, amount pgtype.Numeric, userID uuid.UUID) (db.IncomeEntry, error) {
	if _, err := s.assertPeriodCollaborator(ctx, periodID, userID); err != nil {
		return db.IncomeEntry{}, err
	}
	return s.profiles.UpdateIncomeEntry(ctx, db.UpdateIncomeEntryParams{
		ID:             id,
		BudgetPeriodID: periodID,
		Amount:         amount,
	})
}

// ── Savings Sources ───────────────────────────────────────────────────────────

type SavingsSourceInput struct {
	Name            string
	Amount          pgtype.Numeric
	PaymentMethodID *uuid.UUID
	PaymentDays     []int32 // 1=monthly, 2=bi-weekly, 4=weekly; owner inferred from PM
}

func paymentDaysFrequency(n int) string {
	switch n {
	case 2:
		return "bi_weekly"
	case 4:
		return "weekly"
	default:
		return "monthly"
	}
}

func (s *BudgetProfileService) AddSavingsSource(ctx context.Context, profileID, userID uuid.UUID, inp SavingsSourceInput) (db.SavingsSource, error) {
	if _, err := s.assertCollaboratorOrAbove(ctx, profileID, userID); err != nil {
		return db.SavingsSource{}, err
	}
	n := len(inp.PaymentDays)
	if n != 1 && n != 2 && n != 4 {
		return db.SavingsSource{}, apperr.Invalid("payment_days must have 1, 2, or 4 entries")
	}

	var personID *int32
	if inp.PaymentMethodID != nil {
		pm, err := s.transactions.GetPaymentMethod(ctx, *inp.PaymentMethodID)
		if err != nil {
			return db.SavingsSource{}, err
		}
		personID = pm.BudgetPersonID
	}

	src, err := s.profiles.AddSavingsSource(ctx, db.AddSavingsSourceParams{
		BudgetProfileID: profileID,
		BudgetPersonID:  personID,
		Name:            inp.Name,
		Amount:          inp.Amount,
		Frequency:       paymentDaysFrequency(n),
		PaymentMethodID: inp.PaymentMethodID,
		PaymentDays:     inp.PaymentDays,
	})
	if err != nil {
		return db.SavingsSource{}, err
	}

	s.createSavingsTransactions(ctx, profileID, userID, src)
	return src, nil
}

func (s *BudgetProfileService) createSavingsTransactions(ctx context.Context, profileID, userID uuid.UUID, src db.SavingsSource) {
	period, err := s.profiles.GetLatestPeriod(ctx, profileID)
	if err != nil {
		return
	}
	cats, err := s.transactions.ListCategories(ctx, userID)
	if err != nil {
		return
	}
	savingsCatID := findSystemCategoryID(cats, category.Savings)
	if savingsCatID == nil {
		return
	}

	txTypeID := int32(1) // Fixed
	freqIDByFreq := map[string]int32{"monthly": 4, "bi_weekly": 3, "weekly": 2}
	txFreqID := freqIDByFreq[src.Frequency]

	// Split amount evenly across payment days.
	perDayAmount := src.Amount
	n := len(src.PaymentDays)
	if n > 1 && src.Amount.Int != nil {
		perDayAmount = pgtype.Numeric{
			Int:   new(big.Int).Quo(src.Amount.Int, big.NewInt(int64(n))),
			Exp:   src.Amount.Exp,
			Valid: src.Amount.Valid,
		}
	}

	startTime := period.StartDate.Time
	lastDay := time.Date(startTime.Year(), startTime.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
	for _, day := range src.PaymentDays {
		d := int(day)
		if d > lastDay {
			d = lastDay
		}
		txDate := pgtype.Date{
			Time:  time.Date(startTime.Year(), startTime.Month(), d, 0, 0, 0, 0, time.UTC),
			Valid: true,
		}
		s.transactions.Create(ctx, db.CreateTransactionParams{ //nolint:errcheck
			Name:                   &src.Name,
			Amount:                 perDayAmount,
			PlannedAmount:          perDayAmount,
			Date:                   txDate,
			RenewalDate:            pgtype.Date{},
			BudgetPeriodID:         &period.ID,
			CategoryID:             savingsCatID,
			PaymentMethodID:        src.PaymentMethodID,
			TransactionFrequencyID: &txFreqID,
			TransactionTypeID:      &txTypeID,
		})
	}
}

func (s *BudgetProfileService) ListSavingsSources(ctx context.Context, profileID, userID uuid.UUID) ([]db.SavingsSource, error) {
	if _, err := s.assertMember(ctx, profileID, userID); err != nil {
		return nil, err
	}
	return s.profiles.ListSavingsSources(ctx, profileID)
}

func (s *BudgetProfileService) UpdateSavingsSource(ctx context.Context, id int32, profileID, userID uuid.UUID, inp SavingsSourceInput) (db.SavingsSource, error) {
	if _, err := s.assertCollaboratorOrAbove(ctx, profileID, userID); err != nil {
		return db.SavingsSource{}, err
	}
	n := len(inp.PaymentDays)
	if n != 0 && n != 1 && n != 2 && n != 4 {
		return db.SavingsSource{}, apperr.Invalid("payment_days must have 1, 2, or 4 entries")
	}

	old, err := s.profiles.GetSavingsSource(ctx, db.GetSavingsSourceParams{ID: id, BudgetProfileID: profileID})
	if err != nil {
		return db.SavingsSource{}, err
	}

	var personID *int32
	if inp.PaymentMethodID != nil {
		pm, err := s.transactions.GetPaymentMethod(ctx, *inp.PaymentMethodID)
		if err != nil {
			return db.SavingsSource{}, err
		}
		personID = pm.BudgetPersonID
	}

	freq := paymentDaysFrequency(n)
	if n == 0 {
		freq = "" // preserve existing if no days supplied
	}

	updated, err := s.profiles.UpdateSavingsSource(ctx, db.UpdateSavingsSourceParams{
		ID:              id,
		BudgetProfileID: profileID,
		Name:            inp.Name,
		Amount:          inp.Amount,
		Frequency:       freq,
		BudgetPersonID:  personID,
		PaymentMethodID: inp.PaymentMethodID,
		PaymentDays:     inp.PaymentDays,
	})
	if err != nil {
		return db.SavingsSource{}, err
	}

	// Delete old auto-created transactions, then recreate with updated values.
	if old.PaymentMethodID != nil {
		cats, err := s.transactions.ListCategories(ctx, userID)
		if err == nil {
			if catID := findSystemCategoryID(cats, category.Savings); catID != nil {
				if delErr := s.transactions.DeleteSavingsSourceTransactions(ctx, db.DeleteSavingsSourceTransactionsParams{
					BudgetProfileID: profileID,
					Name:            &old.Name,
					PaymentMethodID: *old.PaymentMethodID,
					CategoryID:      catID,
				}); delErr != nil {
					// Orphaned savings rows keep counting against the budget.
					log.Printf("savings: delete transactions for changed source: %v", delErr)
				}
			}
		}
	}
	if updated.PaymentMethodID != nil && len(updated.PaymentDays) > 0 {
		s.createSavingsTransactions(ctx, profileID, userID, updated)
	}

	return updated, nil
}

func (s *BudgetProfileService) DeleteSavingsSource(ctx context.Context, id int32, profileID, userID uuid.UUID) error {
	if _, err := s.assertCollaboratorOrAbove(ctx, profileID, userID); err != nil {
		return err
	}
	src, err := s.profiles.GetSavingsSource(ctx, db.GetSavingsSourceParams{ID: id, BudgetProfileID: profileID})
	if err != nil {
		return err
	}
	if src.PaymentMethodID != nil {
		cats, err := s.transactions.ListCategories(ctx, userID)
		if err != nil {
			return err
		}
		savingsCatID := findSystemCategoryID(cats, category.Savings)
		if savingsCatID != nil {
			if delErr := s.transactions.DeleteSavingsSourceTransactions(ctx, db.DeleteSavingsSourceTransactionsParams{
				BudgetProfileID: profileID,
				Name:            &src.Name,
				PaymentMethodID: *src.PaymentMethodID,
				CategoryID:      savingsCatID,
			}); delErr != nil {
				// Orphaned savings rows keep counting against the budget.
				log.Printf("savings: delete transactions for removed source: %v", delErr)
			}
		}
	}
	return s.profiles.DeleteSavingsSource(ctx, db.DeleteSavingsSourceParams{
		ID:              id,
		BudgetProfileID: profileID,
	})
}

// ── Fixed Expenses ────────────────────────────────────────────────────────────

type FixedExpenseInput struct {
	Name            string
	PlannedAmount   pgtype.Numeric
	CategoryID      *int32
	PaymentMethodID *uuid.UUID
	DayOfMonth      int32
	IntervalMonths  int32
	AnchorDate      *time.Time // explicit anchor override; overrides DayOfMonth (day is derived from it) when set
	FrequencyUnit   int16      // 0/1 = MONTH (default), 2 = WEEK
	IntervalWeeks   int32      // applies when FrequencyUnit = WEEK
	DayOfWeek       int32      // 1 = Monday ... 7 = Sunday; applies when FrequencyUnit = WEEK
	EndDate         *time.Time // optional payment plan end date; auto-deactivates template after this date
	TotalPayments   int32      // informational: total payments in the plan; 0 = unset
	// Set only by CreateInstallmentPlan. Keeps the template out of Plaid review
	// auto-matching — see scoreBestMatch.
	IsInstallmentPlan bool
}

// isoWeekday converts a date to ISO 8601 weekday numbering (1=Monday..7=Sunday).
func isoWeekday(t time.Time) int {
	d := int(t.Weekday()) // time.Weekday: Sunday=0..Saturday=6
	if d == 0 {
		return 7
	}
	return d
}

// fixedExpenseScheduleFromAnchor derives day_of_month/day_of_week/anchor_date
// from an explicit anchor date — the one place this three-way derivation
// lives, shared by Create/UpdateFixedExpense and markFixedTransactionPaid's
// paid-date propagation (mark_paid.go). Both day fields are always derived
// together regardless of the expense's FrequencyUnit; which one scheduling
// actually reads depends on that unit, not on which was set here.
func fixedExpenseScheduleFromAnchor(anchor time.Time) (dayOfMonth, dayOfWeek int32, anchorDate pgtype.Date) {
	return int32(anchor.Day()), int32(isoWeekday(anchor)), pgtype.Date{Time: anchor, Valid: true}
}

func (s *BudgetProfileService) CreateFixedExpense(ctx context.Context, profileID, userID uuid.UUID, inp FixedExpenseInput) (db.FixedExpense, *db.Transaction, error) {
	if _, err := s.assertCollaboratorOrAbove(ctx, profileID, userID); err != nil {
		return db.FixedExpense{}, nil, err
	}
	unit := inp.FrequencyUnit
	if unit != frequencyUnitWeek {
		unit = frequencyUnitMonth
	}
	day := inp.DayOfMonth
	if day < 1 {
		day = 1
	}
	dayOfWeek := inp.DayOfWeek
	if dayOfWeek < 1 || dayOfWeek > 7 {
		dayOfWeek = 1
	}
	anchorDate := pgtype.Date{}
	if inp.AnchorDate != nil {
		day, dayOfWeek, anchorDate = fixedExpenseScheduleFromAnchor(*inp.AnchorDate)
	}
	interval := inp.IntervalMonths
	if interval < 1 {
		interval = 1
	}
	intervalWeeks := inp.IntervalWeeks
	if intervalWeeks < 1 {
		intervalWeeks = 1
	}
	endDate := pgtype.Date{}
	if inp.EndDate != nil {
		endDate = pgtype.Date{Time: *inp.EndDate, Valid: true}
	}
	var totalPayments *int32
	if inp.TotalPayments > 0 {
		totalPayments = &inp.TotalPayments
	}
	fe, err := s.fixedExpenses.Create(ctx, db.CreateFixedExpenseParams{
		BudgetProfileID:   profileID,
		Name:              inp.Name,
		PlannedAmount:     inp.PlannedAmount,
		CategoryID:        inp.CategoryID,
		PaymentMethodID:   inp.PaymentMethodID,
		DayOfMonth:        day,
		IntervalMonths:    interval,
		AnchorDate:        anchorDate,
		FrequencyUnit:     unit,
		IntervalWeeks:     intervalWeeks,
		DayOfWeek:         int16(dayOfWeek),
		EndDate:           endDate,
		TotalPayments:     totalPayments,
		IsInstallmentPlan: inp.IsInstallmentPlan,
	})
	if err != nil {
		return db.FixedExpense{}, nil, err
	}

	// Spawn transaction(s) in the active period, unless an explicit anchor
	// date makes it not due yet (e.g. a future-dated subscription start).
	period, err := s.profiles.GetLatestPeriod(ctx, profileID)
	if err != nil {
		return fe, nil, nil // no active period — still return the expense
	}
	startDate := period.StartDate.Time

	if unit == frequencyUnitWeek {
		// A week-unit expense can have several due occurrences inside the
		// current period (e.g. "every week"); spawnWeeklyFixedExpenseOccurrences
		// already skips any non-due week internally (naturally handles a
		// future anchor too, same as the month-unit due-check below), so no
		// separate gate is needed here. There's no single transaction to
		// return in the response — the caller relies on re-fetching
		// transactions afterward, same as createNextPeriod's spawn path.
		// nil active-set: the payment method came from the request the user
		// just submitted, and the picker only offers live ones. Only the
		// unattended rollover needs to re-check a template it didn't write.
		s.spawnWeeklyFixedExpenseOccurrences(ctx, fe, period.ID, startDate, period.EndDate.Time, nil)
		return fe, nil, nil
	}

	if inp.AnchorDate != nil && !isFixedExpenseDueInMonth(fe, startDate) {
		return fe, nil, nil // future-dated; no transaction until due
	}
	lastDay := time.Date(startDate.Year(), startDate.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
	d := int(day)
	if d > lastDay {
		d = lastDay
	}
	txDate := pgtype.Date{
		Time:  time.Date(startDate.Year(), startDate.Month(), d, 0, 0, 0, 0, time.UTC),
		Valid: true,
	}
	txTypeFixed := int32(1)
	feID := fe.ID
	name := fe.Name
	tx, txErr := s.transactions.Create(ctx, db.CreateTransactionParams{
		Name:              &name,
		Amount:            fe.PlannedAmount,
		PlannedAmount:     fe.PlannedAmount,
		Date:              txDate,
		BudgetPeriodID:    &period.ID,
		CategoryID:        fe.CategoryID,
		PaymentMethodID:   fe.PaymentMethodID,
		TransactionTypeID: &txTypeFixed,
		FixedExpenseID:    &feID,
	})
	if txErr != nil {
		return fe, nil, nil
	}
	return fe, &tx, nil
}

// InstallmentPlanInput describes converting one variable transaction into a
// fixed-expense payment plan.
type InstallmentPlanInput struct {
	TransactionID    uuid.UUID
	BudgetPeriodID   uuid.UUID
	FirstPaymentDate time.Time
	TotalPayments    int32
	EndDate          *time.Time // optional override; derived from TotalPayments when nil
}

// CreateInstallmentPlan turns a variable transaction into a card installment
// plan: the purchase stops counting toward this period's totals, and a fixed
// expense takes its place spread across TotalPayments payments.
//
// Deliberately permitted on an ARCHIVED period, unlike every other write to a
// transaction. Scoping is by profile (assertCollaboratorOrAbove) rather than by
// period, so the archived guard in TransactionService.assertPeriodCollaborator
// is never reached. Realising two months later that a purchase was actually
// financed is the normal case, not an edge case — the plan's payments land in
// current and future periods regardless of when the purchase happened.
//
// Order matters and is not transactional: the plan is created first, then the
// transaction is excluded and linked. A failure between the two leaves a
// visible plan next to a still-counting purchase, which the user can see and
// resolve. The reverse order would leave a silently excluded transaction with
// nothing to explain it.
func (s *BudgetProfileService) CreateInstallmentPlan(ctx context.Context, userID uuid.UUID, inp InstallmentPlanInput) (db.FixedExpense, db.Transaction, error) {
	if inp.TotalPayments < 2 {
		return db.FixedExpense{}, db.Transaction{}, apperr.Invalid("an installment plan needs at least 2 payments")
	}

	period, err := s.profiles.GetPeriodByID(ctx, inp.BudgetPeriodID)
	if err != nil {
		return db.FixedExpense{}, db.Transaction{}, err
	}
	if _, err := s.assertCollaboratorOrAbove(ctx, period.BudgetProfileID, userID); err != nil {
		return db.FixedExpense{}, db.Transaction{}, err
	}

	tx, err := s.transactions.GetByID(ctx, inp.TransactionID)
	if err != nil {
		return db.FixedExpense{}, db.Transaction{}, err
	}
	if tx.BudgetPeriodID == nil || *tx.BudgetPeriodID != inp.BudgetPeriodID {
		return db.FixedExpense{}, db.Transaction{}, apperr.NotFound("transaction", inp.TransactionID.String())
	}
	if tx.InstallmentFixedExpenseID != nil {
		return db.FixedExpense{}, db.Transaction{}, apperr.Invalid("this transaction is already an installment plan")
	}
	// A Fixed transaction is already the recurring thing this would create.
	if tx.TransactionTypeID != nil && *tx.TransactionTypeID == fixedTransactionTypeID {
		return db.FixedExpense{}, db.Transaction{}, apperr.Invalid("only a variable transaction can be split into installments")
	}
	// A received amount is stored negative (docs/features/negative-positive-transactions.md).
	// Splitting money that came in across future payments is not a thing.
	if numericToNanos(tx.Amount) <= 0 {
		return db.FixedExpense{}, db.Transaction{}, apperr.Invalid("only a spend can be split into installments")
	}

	endDate := installmentEndDate(inp.FirstPaymentDate, inp.TotalPayments)
	if inp.EndDate != nil {
		endDate = *inp.EndDate
	}
	if endDate.Before(inp.FirstPaymentDate) {
		return db.FixedExpense{}, db.Transaction{}, apperr.Invalid("the plan cannot end before its first payment")
	}

	name := ""
	if tx.Name != nil {
		name = *tx.Name
	}
	anchor := inp.FirstPaymentDate

	fe, _, err := s.CreateFixedExpense(ctx, period.BudgetProfileID, userID, FixedExpenseInput{
		Name:              name,
		PlannedAmount:     installmentAmount(tx.Amount, inp.TotalPayments),
		CategoryID:        tx.CategoryID,
		PaymentMethodID:   tx.PaymentMethodID,
		AnchorDate:        &anchor,
		IntervalMonths:    1,
		EndDate:           &endDate,
		TotalPayments:     inp.TotalPayments,
		IsInstallmentPlan: true,
	})
	if err != nil {
		return db.FixedExpense{}, db.Transaction{}, err
	}

	updated, err := s.transactions.SetInstallmentPlan(ctx, db.SetTransactionInstallmentPlanParams{
		ID:                        inp.TransactionID,
		BudgetPeriodID:            inp.BudgetPeriodID,
		InstallmentFixedExpenseID: fe.ID,
	})
	if err != nil {
		return db.FixedExpense{}, db.Transaction{}, err
	}
	return fe, updated, nil
}

// DeleteInstallmentPlan reverses CreateInstallmentPlan: the plan and every
// payment it spawned are deleted, and the original purchase counts again.
//
// Deleting the spawned payments is not optional. Leaving them would have the
// purchase counting AND its payments counting alongside it — the exact
// double-count the link on the transaction exists to prevent.
//
// Refused once any payment has been marked paid. That is real recorded spend,
// and losing it to an undo is worse than making the user unmark it first. It
// also keeps this from becoming a way to delete transactions out of an
// archived period, which nothing else permits.
//
// Scoped by profile like CreateInstallmentPlan, so undoing a split works on an
// archived period exactly as making one does.
func (s *BudgetProfileService) DeleteInstallmentPlan(ctx context.Context, txID, periodID, userID uuid.UUID) (db.Transaction, error) {
	period, err := s.profiles.GetPeriodByID(ctx, periodID)
	if err != nil {
		return db.Transaction{}, err
	}
	if _, err := s.assertCollaboratorOrAbove(ctx, period.BudgetProfileID, userID); err != nil {
		return db.Transaction{}, err
	}

	tx, err := s.transactions.GetByID(ctx, txID)
	if err != nil {
		return db.Transaction{}, err
	}
	if tx.InstallmentFixedExpenseID == nil {
		return db.Transaction{}, apperr.Invalid("this transaction is not an installment plan")
	}
	feID := *tx.InstallmentFixedExpenseID

	spawned, err := s.transactions.ListByFixedExpense(ctx, feID)
	if err != nil {
		return db.Transaction{}, err
	}
	for _, p := range spawned {
		if p.IsPaid {
			return db.Transaction{}, apperr.Invalid("this plan already has a payment marked paid; unmark it before undoing the split")
		}
	}

	// Clear the link first. If a later step fails the user is left with a
	// counting purchase beside a still-live plan, which is visible and
	// fixable; clearing it last could strand an excluded transaction pointing
	// at a plan that no longer exists.
	updated, err := s.transactions.ClearInstallmentPlan(ctx, db.ClearTransactionInstallmentPlanParams{
		ID:             txID,
		BudgetPeriodID: periodID,
	})
	if err != nil {
		return db.Transaction{}, err
	}
	if err := s.transactions.DeleteByFixedExpense(ctx, feID); err != nil {
		return db.Transaction{}, err
	}
	// Deactivate, not a hard delete — this repo soft-deletes fixed expenses
	// everywhere else, and with its payments gone and the purchase unlinked the
	// template is already invisible (ListFixedExpenses returns active only).
	if err := s.fixedExpenses.Deactivate(ctx, db.DeactivateFixedExpenseParams{
		ID:              feID,
		BudgetProfileID: period.BudgetProfileID,
	}); err != nil {
		return db.Transaction{}, err
	}
	return updated, nil
}

func (s *BudgetProfileService) ListFixedExpenses(ctx context.Context, profileID, userID uuid.UUID) ([]db.FixedExpense, error) {
	if _, err := s.assertMember(ctx, profileID, userID); err != nil {
		return nil, err
	}
	return s.fixedExpenses.List(ctx, profileID)
}

func (s *BudgetProfileService) UpdateFixedExpense(ctx context.Context, id uuid.UUID, profileID, userID uuid.UUID, inp FixedExpenseInput) (db.FixedExpense, error) {
	if _, err := s.assertCollaboratorOrAbove(ctx, profileID, userID); err != nil {
		return db.FixedExpense{}, err
	}
	unit := inp.FrequencyUnit
	if unit != frequencyUnitWeek {
		unit = frequencyUnitMonth
	}
	day := inp.DayOfMonth
	if day < 1 {
		day = 1
	}
	dayOfWeek := inp.DayOfWeek
	if dayOfWeek < 1 || dayOfWeek > 7 {
		dayOfWeek = 1
	}
	anchorDate := pgtype.Date{}
	if inp.AnchorDate != nil {
		day, dayOfWeek, anchorDate = fixedExpenseScheduleFromAnchor(*inp.AnchorDate)
	}
	interval := inp.IntervalMonths
	if interval < 1 {
		interval = 1
	}
	intervalWeeks := inp.IntervalWeeks
	if intervalWeeks < 1 {
		intervalWeeks = 1
	}
	endDate := pgtype.Date{}
	if inp.EndDate != nil {
		endDate = pgtype.Date{Time: *inp.EndDate, Valid: true}
	}
	var totalPayments *int32
	if inp.TotalPayments > 0 {
		totalPayments = &inp.TotalPayments
	}
	fe, err := s.fixedExpenses.Update(ctx, db.UpdateFixedExpenseParams{
		ID:              id,
		BudgetProfileID: profileID,
		Name:            inp.Name,
		PlannedAmount:   inp.PlannedAmount,
		CategoryID:      inp.CategoryID,
		PaymentMethodID: inp.PaymentMethodID,
		DayOfMonth:      day,
		IntervalMonths:  interval,
		AnchorDate:      anchorDate,
		FrequencyUnit:   unit,
		IntervalWeeks:   intervalWeeks,
		DayOfWeek:       int16(dayOfWeek),
		EndDate:         endDate,
		TotalPayments:   totalPayments,
	})
	if err != nil {
		return db.FixedExpense{}, err
	}

	// WEEK unit can have several transactions per period (not just one), so
	// the "reconcile the current period's single unpaid transaction" model
	// below doesn't apply — editing a WEEK-unit template only affects future
	// spawns, it does not retroactively touch already-spawned transactions.
	if unit == frequencyUnitWeek {
		return fe, nil
	}

	// Reconcile the unpaid transaction in the active period: propagate field
	// changes if fe is still due this period, or remove it if the update
	// (e.g. a rescheduled anchor) made it no longer due.
	period, err := s.profiles.GetLatestPeriod(ctx, profileID)
	if err != nil {
		return fe, nil
	}
	startDate := period.StartDate.Time
	if !isFixedExpenseDueInMonth(fe, startDate) {
		if delErr := s.fixedExpenses.DeleteUnpaidTransactions(ctx, db.DeleteUnpaidTransactionByFixedExpenseParams{
			FixedExpenseID:  fe.ID,
			BudgetProfileID: profileID,
		}); delErr != nil {
			// A stale unpaid bill outlives the template change that should have replaced it.
			log.Printf("fixed_expense: delete unpaid transactions: %v", delErr)
		}
		return fe, nil
	}
	lastDay := time.Date(startDate.Year(), startDate.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
	d := int(day)
	if d > lastDay {
		d = lastDay
	}
	txDate := pgtype.Date{
		Time:  time.Date(startDate.Year(), startDate.Month(), d, 0, 0, 0, 0, time.UTC),
		Valid: true,
	}
	name := fe.Name
	// Ask whether the bill exists at all, not whether it is unpaid. Asking for
	// the unpaid one made an already-paid bill look absent, so editing a bill
	// after marking it paid fell into the spawn branch below and produced a
	// second transaction for the same fixed expense in the same period.
	existing, getErr := s.fixedExpenses.GetTransaction(ctx, db.GetTransactionByFixedExpenseParams{
		FixedExpenseID:  fe.ID,
		BudgetProfileID: profileID,
	})
	switch {
	case getErr != nil:
		// No existing transaction — expense just became due (e.g. anchor
		// date was removed or moved to past). Spawn one for the current period.
		txTypeFixed := int32(1)
		feID := fe.ID
		if _, spawnErr := s.transactions.Create(ctx, db.CreateTransactionParams{
			Name:              &name,
			Amount:            fe.PlannedAmount,
			PlannedAmount:     fe.PlannedAmount,
			Date:              txDate,
			BudgetPeriodID:    &period.ID,
			CategoryID:        fe.CategoryID,
			PaymentMethodID:   fe.PaymentMethodID,
			TransactionTypeID: &txTypeFixed,
			FixedExpenseID:    &feID,
		}); spawnErr != nil {
			// A bill the user owes this period simply never appears.
			log.Printf("period_rollover: spawn fixed expense %s into period %s: %v", feID, period.ID, spawnErr)
		}
	case existing.IsPaid:
		// The bill is settled, so only the descriptive fields follow the
		// template. Amount, planned amount and the paid flag stay as recorded:
		// what was paid is a fact about this period, and re-planning it belongs
		// to future periods only (docs/features/planned-amount-follows-paid.md).
		// Date stays too — it is when the payment happened.
		if syncErr := s.fixedExpenses.UpdatePaidTransactionFromFixedExpense(ctx, db.UpdatePaidTransactionFromFixedExpenseParams{
			FixedExpenseID:  fe.ID,
			BudgetProfileID: profileID,
			Name:            &name,
			CategoryID:      fe.CategoryID,
			PaymentMethodID: fe.PaymentMethodID,
		}); syncErr != nil {
			// The paid row keeps the old category/payment method while the
			// template shows the new one.
			log.Printf("fixed_expense: sync paid current period transaction: %v", syncErr)
		}
	default:
		if syncErr := s.fixedExpenses.UpdateTransactionFromFixedExpense(ctx, db.UpdateTransactionFromFixedExpenseParams{
			FixedExpenseID:  fe.ID,
			BudgetProfileID: profileID,
			Name:            &name,
			PlannedAmount:   fe.PlannedAmount,
			CategoryID:      fe.CategoryID,
			PaymentMethodID: fe.PaymentMethodID,
			Date:            txDate,
		}); syncErr != nil {
			// The current period keeps the old amount while the template shows the new one.
			log.Printf("fixed_expense: sync current period transaction: %v", syncErr)
		}
	}

	return fe, nil
}

func (s *BudgetProfileService) DeleteFixedExpense(ctx context.Context, id uuid.UUID, profileID, userID uuid.UUID) error {
	if _, err := s.assertCollaboratorOrAbove(ctx, profileID, userID); err != nil {
		return err
	}
	// Verify ownership: the expense must belong to this profile.
	fe, err := s.fixedExpenses.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if fe.BudgetProfileID != profileID {
		return apperr.Forbidden("access denied")
	}

	// Remove unpaid transaction from the active period.
	if delErr := s.fixedExpenses.DeleteUnpaidTransactions(ctx, db.DeleteUnpaidTransactionByFixedExpenseParams{
		FixedExpenseID:  id,
		BudgetProfileID: profileID,
	}); delErr != nil {
		// A deactivated template's unpaid bill stays on the period.
		log.Printf("fixed_expense: delete unpaid transactions on deactivate: %v", delErr)
	}

	return s.fixedExpenses.Deactivate(ctx, db.DeactivateFixedExpenseParams{
		ID:              id,
		BudgetProfileID: profileID,
	})
}

// ── Period date helpers ───────────────────────────────────────────────────────

// activePaymentMethodIDs is the set of payment methods a spawned bill may be
// attributed to. ListPaymentMethods already filters to is_active.
//
// A failure returns nil, which livePaymentMethod reads as "don't second-guess
// the template" — losing attribution on every bill in a period because one
// lookup failed would be worse than the problem this guards against.
func (s *BudgetProfileService) activePaymentMethodIDs(ctx context.Context, profileID uuid.UUID) map[uuid.UUID]bool {
	methods, err := s.transactions.ListPaymentMethods(ctx, profileID)
	if err != nil {
		log.Printf("period_rollover: list payment methods for profile %s: %v", profileID, err)
		return nil
	}
	active := make(map[uuid.UUID]bool, len(methods))
	for _, m := range methods {
		active[m.ID] = true
	}
	return active
}

// livePaymentMethod drops a template's payment method when that method has been
// deactivated, so the spawned bill lands unattributed rather than on an account
// the user has removed.
//
// Deleting a payment method reassigns its fixed expenses to the replacement the
// user picked (DeletePaymentMethodAndReassign), but the Plaid refresh path
// deactivates a method with no replacement to offer -- and the template kept
// pointing at it, so the bill respawned onto a dead account every month. Four
// removed accounts on one production budget were still collecting bills that
// way.
//
// nil active means the lookup failed; leave the template's own value alone.
func livePaymentMethod(id *uuid.UUID, active map[uuid.UUID]bool) *uuid.UUID {
	if id == nil || active == nil || active[*id] {
		return id
	}
	return nil
}

func computeFirstPeriodDates(cycle string) (start, end time.Time) {
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	switch cycle {
	case "weekly":
		return today, today.AddDate(0, 0, 6)
	case "bi_weekly":
		return today, today.AddDate(0, 0, 13)
	case "yearly":
		s := time.Date(now.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
		return s, time.Date(now.Year(), 12, 31, 0, 0, 0, 0, time.UTC)
	default: // monthly
		s := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		e := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1)
		return s, e
	}
}

func computeNextPeriodDates(cycle string, prevEnd time.Time) (start, end time.Time) {
	start = prevEnd.AddDate(0, 0, 1)
	switch cycle {
	case "weekly":
		return start, start.AddDate(0, 0, 6)
	case "bi_weekly":
		return start, start.AddDate(0, 0, 13)
	case "yearly":
		e := time.Date(start.Year(), 12, 31, 0, 0, 0, 0, start.Location())
		return start, e
	default: // monthly
		e := time.Date(start.Year(), start.Month()+1, 1, 0, 0, 0, 0, start.Location()).AddDate(0, 0, -1)
		return start, e
	}
}
