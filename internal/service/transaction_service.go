package service

import (
	"context"
	"log"

	"github.com/BeWellSpent/wellspent-backend/internal/apperr"
	"github.com/BeWellSpent/wellspent-backend/internal/repository"
	db "github.com/BeWellSpent/wellspent-backend/internal/sqlc"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

type TransactionService struct {
	transactions  repository.TransactionRepository
	profiles      repository.BudgetProfileRepository
	allocations   repository.ExpenseAllocationRepository
	fixedExpenses repository.FixedExpenseRepository
	reviews       repository.TransactionReviewRepository
	notifs        *NotificationService
}

func NewTransactionService(transactions repository.TransactionRepository, profiles repository.BudgetProfileRepository, allocations repository.ExpenseAllocationRepository, fixedExpenses repository.FixedExpenseRepository, reviews repository.TransactionReviewRepository) *TransactionService {
	if transactions == nil {
		panic("NewTransactionService: transactions is required")
	}
	if profiles == nil {
		panic("NewTransactionService: profiles is required")
	}
	if allocations == nil {
		panic("NewTransactionService: allocations is required")
	}
	if fixedExpenses == nil {
		panic("NewTransactionService: fixedExpenses is required")
	}
	if reviews == nil {
		panic("NewTransactionService: reviews is required")
	}
	return &TransactionService{transactions: transactions, profiles: profiles, allocations: allocations, fixedExpenses: fixedExpenses, reviews: reviews}
}

func (s *TransactionService) WithNotifications(ns *NotificationService) *TransactionService {
	s.notifs = ns
	return s
}

// getUserRoleForPeriod returns the caller's effective role for the budget profile
// that owns the given period (and the period itself, so callers that also need
// it — e.g. assertPeriodCollaborator's archived/backdating checks — don't have
// to re-fetch it). Profile owners are always "admin".
func (s *TransactionService) getUserRoleForPeriod(ctx context.Context, periodID, userID uuid.UUID) (db.BudgetPeriod, string, error) {
	period, err := s.profiles.GetPeriodByID(ctx, periodID)
	if err != nil {
		return db.BudgetPeriod{}, "", err
	}
	profile, err := s.profiles.GetByID(ctx, period.BudgetProfileID)
	if err != nil {
		return db.BudgetPeriod{}, "", err
	}
	if profile.UserID == userID {
		return period, "admin", nil
	}
	person, err := s.profiles.GetPersonByUserID(ctx, profile.ID, userID)
	if err != nil {
		return db.BudgetPeriod{}, "", apperr.Forbidden("access denied")
	}
	return period, person.Role, nil
}

func (s *TransactionService) assertPeriodMember(ctx context.Context, periodID, userID uuid.UUID) error {
	_, _, err := s.getUserRoleForPeriod(ctx, periodID, userID)
	return err
}

// assertPeriodCollaborator checks role membership and rejects a write against
// an archived (read-only) period, returning the period itself so callers that
// also need to validate the transaction's date (Create/Update) don't have to
// re-fetch it.
func (s *TransactionService) assertPeriodCollaborator(ctx context.Context, periodID, userID uuid.UUID) (db.BudgetPeriod, error) {
	period, role, err := s.getUserRoleForPeriod(ctx, periodID, userID)
	if err != nil {
		return db.BudgetPeriod{}, err
	}
	if role != "admin" && role != "collaborator" {
		return db.BudgetPeriod{}, apperr.Forbidden("access denied")
	}
	if period.IsArchived {
		return db.BudgetPeriod{}, apperr.Forbidden("this budget period is archived and read-only")
	}
	return period, nil
}

// assertNotBackdated rejects a Variable transaction whose date falls before
// the period's own start_date — i.e. a date that actually belongs to a
// previous (already-archived) period. Fixed-type transactions are exempt:
// their occurrences are template-driven (spawned by createNextPeriod, or
// created directly against a specific period), not manually dated by the
// user in the same sense a Variable spend is.
func assertNotBackdated(transactionTypeID *int32, date pgtype.Date, period db.BudgetPeriod) error {
	isFixed := transactionTypeID != nil && *transactionTypeID == 1
	if isFixed || !date.Valid || !period.StartDate.Valid {
		return nil
	}
	if date.Time.Before(period.StartDate.Time) {
		return apperr.Invalid("transaction date falls within an archived period")
	}
	return nil
}

// assertOnlyCategoryChanged rejects an update unless it differs from the
// existing transaction only in CategoryID — used for Plaid-imported
// transactions and any transaction whose period has archived, where the
// financial record itself must stay frozen but re-categorizing is still
// allowed.
func assertOnlyCategoryChanged(arg db.UpdateTransactionParams, existing db.Transaction) error {
	if equalOptString(arg.Name, existing.Name) &&
		equalNumeric(arg.Amount, existing.Amount) &&
		equalNumeric(arg.PlannedAmount, existing.PlannedAmount) &&
		equalDate(arg.Date, existing.Date) &&
		equalOptUUID(arg.PaymentMethodID, existing.PaymentMethodID) &&
		equalOptInt32(arg.TransactionFrequencyID, existing.TransactionFrequencyID) &&
		equalOptInt32(arg.TransactionTypeID, existing.TransactionTypeID) {
		return nil
	}
	return apperr.Invalid("only the category can be changed for this transaction")
}

func equalOptString(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func equalOptInt32(a, b *int32) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func equalOptUUID(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// equalNumeric compares by value (via Float64Value) rather than by raw
// Int/Exp representation — a client resubmitting an unchanged amount after
// round-tripping it through display formatting can produce a structurally
// different but numerically identical pgtype.Numeric (e.g. Int=105,Exp=-1
// vs Int=1050,Exp=-2, both 10.50), which a raw field comparison would
// wrongly treat as a change.
func equalNumeric(a, b pgtype.Numeric) bool {
	if a.Valid != b.Valid {
		return false
	}
	if !a.Valid {
		return true
	}
	af, aErr := a.Float64Value()
	bf, bErr := b.Float64Value()
	if aErr != nil || bErr != nil {
		return false
	}
	return af.Float64 == bf.Float64
}

func equalDate(a, b pgtype.Date) bool {
	if a.Valid != b.Valid {
		return false
	}
	if !a.Valid {
		return true
	}
	return a.Time.Equal(b.Time)
}

func (s *TransactionService) getUserRoleForProfile(ctx context.Context, profileID, userID uuid.UUID) (string, error) {
	profile, err := s.profiles.GetByID(ctx, profileID)
	if err != nil {
		return "", err
	}
	if profile.UserID == userID {
		return "admin", nil
	}
	person, err := s.profiles.GetPersonByUserID(ctx, profileID, userID)
	if err != nil {
		return "", apperr.Forbidden("access denied")
	}
	return person.Role, nil
}

func (s *TransactionService) assertProfileMember(ctx context.Context, profileID, userID uuid.UUID) error {
	_, err := s.getUserRoleForProfile(ctx, profileID, userID)
	return err
}

func (s *TransactionService) assertProfileCollaborator(ctx context.Context, profileID, userID uuid.UUID) error {
	role, err := s.getUserRoleForProfile(ctx, profileID, userID)
	if err != nil {
		return err
	}
	if role != "admin" && role != "collaborator" {
		return apperr.Forbidden("access denied")
	}
	return nil
}

func (s *TransactionService) GetByID(ctx context.Context, id uuid.UUID) (db.Transaction, error) {
	return s.transactions.GetByID(ctx, id)
}

func (s *TransactionService) List(ctx context.Context, arg db.ListTransactionsParams, userID uuid.UUID) ([]db.Transaction, error) {
	if err := s.assertPeriodMember(ctx, arg.BudgetPeriodID, userID); err != nil {
		return nil, err
	}
	return s.transactions.List(ctx, arg)
}

func (s *TransactionService) Create(ctx context.Context, arg db.CreateTransactionParams, userID uuid.UUID) (db.Transaction, error) {
	if arg.BudgetPeriodID != nil {
		period, err := s.assertPeriodCollaborator(ctx, *arg.BudgetPeriodID, userID)
		if err != nil {
			return db.Transaction{}, err
		}
		if err := assertNotBackdated(arg.TransactionTypeID, arg.Date, period); err != nil {
			return db.Transaction{}, err
		}
	}
	tx, err := s.transactions.Create(ctx, arg)
	if err != nil {
		return db.Transaction{}, err
	}
	isVariable := arg.TransactionTypeID != nil && *arg.TransactionTypeID == 2
	if isVariable && arg.BudgetPeriodID != nil {
		s.maybeQueueReview(ctx, tx, *arg.BudgetPeriodID)
		if s.notifs != nil {
			s.notifs.HandleNewTransaction(ctx, tx, *arg.BudgetPeriodID, userID)
		}
	}
	return tx, nil
}

// maybeQueueReview scores a newly-created variable transaction against all
// active fixed expenses in the budget. If the best match scores ≥ 80 and
// there is an unpaid fixed transaction for that expense in the period, a
// review entry is upserted so the user can confirm or dismiss it from the
// To Review tab. All failures are silently ignored — the transaction has
// already been saved successfully and review queueing is best-effort.
func (s *TransactionService) maybeQueueReview(ctx context.Context, tx db.Transaction, periodID uuid.UUID) {
	if tx.Name == nil {
		return
	}
	period, err := s.profiles.GetPeriodByID(ctx, periodID)
	if err != nil {
		return
	}
	fixedExpenses, err := s.fixedExpenses.List(ctx, period.BudgetProfileID)
	if err != nil || len(fixedExpenses) == 0 {
		return
	}
	aliasesByFE := make(map[uuid.UUID][]string, len(fixedExpenses))
	for _, fe := range fixedExpenses {
		aliases, _ := s.reviews.ListAliases(ctx, fe.ID)
		aliasesByFE[fe.ID] = aliases
	}
	amountF64 := 0.0
	if v, vErr := tx.Amount.Float64Value(); vErr == nil && v.Valid {
		amountF64 = v.Float64
	}
	bestScore, bestFE := scoreBestMatch(*tx.Name, amountF64, tx.CategoryID, tx.PaymentMethodID, fixedExpenses, aliasesByFE)
	if bestScore < 80 || bestFE == nil {
		return
	}
	// Same-period only, matching MarkTransactionForReview's guard — a review
	// linking two different periods can't be rendered by either client.
	unpaid, upErr := s.fixedExpenses.GetUnpaidTransactionInPeriod(ctx, db.GetUnpaidTransactionByFixedExpenseInPeriodParams{
		FixedExpenseID: bestFE.ID,
		BudgetPeriodID: periodID,
	})
	if upErr != nil || unpaid.BudgetPeriodID == nil {
		return
	}
	if _, reviewErr := s.reviews.Upsert(ctx, periodID, tx.ID, unpaid.ID, bestScore); reviewErr != nil {
		// Not fatal: the transaction is created and correct, it just won't be
		// offered for review. Logged because silence here is what made a missing
		// review look like the matcher simply not firing.
		log.Printf("transaction.create: queue review for transaction %s against %s: %v", tx.ID, unpaid.ID, reviewErr)
	}
	if s.notifs != nil && tx.Name != nil {
		s.notifs.HandleReviewPending(ctx, period.BudgetProfileID, *tx.Name)
	}
}

// Update allows a full edit of a transaction, with two exceptions where only
// its category may still change: once a transaction's period has archived
// (its financial record should stay frozen, but re-categorizing for
// reporting is still useful), and for any Plaid-imported transaction at any
// time (it's synced data, not something the user entered by hand — amount,
// date, and payment method should reflect what the bank actually reported).
// Unlike Create/Delete/MarkTransactionAsPaid/etc., archiving does NOT hard-
// block Update entirely, so this deliberately does not call
// assertPeriodCollaborator (which would reject archived-period writes
// outright) — it does the same role check directly instead.
func (s *TransactionService) Update(ctx context.Context, arg db.UpdateTransactionParams, userID uuid.UUID) (db.Transaction, error) {
	tx, err := s.transactions.GetByID(ctx, arg.ID)
	if err != nil {
		return db.Transaction{}, err
	}
	if tx.BudgetPeriodID != nil {
		period, role, err := s.getUserRoleForPeriod(ctx, *tx.BudgetPeriodID, userID)
		if err != nil {
			return db.Transaction{}, err
		}
		if role != "admin" && role != "collaborator" {
			return db.Transaction{}, apperr.Forbidden("access denied")
		}
		if period.IsArchived || tx.PlaidTransactionID != nil {
			if err := assertOnlyCategoryChanged(arg, tx); err != nil {
				return db.Transaction{}, err
			}
		} else if err := assertNotBackdated(arg.TransactionTypeID, arg.Date, period); err != nil {
			return db.Transaction{}, err
		}
	}
	return s.transactions.Update(ctx, arg)
}

func (s *TransactionService) Delete(ctx context.Context, id, userID uuid.UUID) error {
	tx, err := s.transactions.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if tx.BudgetPeriodID != nil {
		if _, err := s.assertPeriodCollaborator(ctx, *tx.BudgetPeriodID, userID); err != nil {
			return err
		}
	}
	// Synced bank data is hard to recover once deleted — block it entirely, same as edits.
	if tx.PlaidTransactionID != nil {
		return apperr.Invalid("Plaid-imported transactions cannot be deleted")
	}
	return s.transactions.Delete(ctx, db.DeleteTransactionParams{ID: id, BudgetPeriodID: tx.BudgetPeriodID})
}

func (s *TransactionService) GetCategory(ctx context.Context, id int32) (db.GetCategoryRow, error) {
	return s.transactions.GetCategory(ctx, id)
}

func (s *TransactionService) ListCategories(ctx context.Context, userID uuid.UUID, budgetProfileID *uuid.UUID) ([]db.ListCategoriesRow, error) {
	if budgetProfileID != nil {
		return s.transactions.ListCategoriesForBudget(ctx, userID, *budgetProfileID)
	}
	return s.transactions.ListCategories(ctx, userID)
}

func (s *TransactionService) CreateCategory(ctx context.Context, arg db.CreateCategoryParams) (db.CreateCategoryRow, error) {
	return s.transactions.CreateCategory(ctx, arg)
}

func (s *TransactionService) UpdateCategory(ctx context.Context, arg db.UpdateCategoryParams) (db.UpdateCategoryRow, error) {
	cat, err := s.transactions.GetCategory(ctx, arg.ID)
	if err != nil {
		return db.UpdateCategoryRow{}, err
	}
	if cat.IsSystem {
		row, err := s.transactions.UpdateSystemCategoryColor(ctx, db.UpdateSystemCategoryColorParams{
			ID:    arg.ID,
			Color: arg.Color,
		})
		if err != nil {
			return db.UpdateCategoryRow{}, err
		}
		return db.UpdateCategoryRow{
			ID:       row.ID,
			Name:     row.Name,
			TypeID:   row.TypeID,
			IsSystem: row.IsSystem,
			UserID:   row.UserID,
			Color:    row.Color,
		}, nil
	}
	return s.transactions.UpdateCategory(ctx, arg)
}

func (s *TransactionService) DeleteCategory(ctx context.Context, id, replacementID int32, userID uuid.UUID) error {
	cat, err := s.transactions.GetCategory(ctx, id)
	if err != nil {
		return err
	}
	if cat.IsSystem {
		return apperr.Forbidden("system categories cannot be deleted")
	}
	if cat.UserID == nil || *cat.UserID != userID {
		return apperr.Forbidden("access denied")
	}
	replacement, err := s.transactions.GetCategory(ctx, replacementID)
	if err != nil {
		return err
	}
	if replacement.UserID != nil && *replacement.UserID != userID {
		return apperr.Forbidden("replacement category is not accessible")
	}
	return s.transactions.DeleteCategoryAndReassign(ctx, db.DeleteCategoryAndReassignParams{
		ID:            id,
		UserID:        userID,
		ReplacementID: &replacementID,
	})
}

func (s *TransactionService) ListPaymentMethods(ctx context.Context, budgetProfileID uuid.UUID) ([]db.ListPaymentMethodsRow, error) {
	return s.transactions.ListPaymentMethods(ctx, budgetProfileID)
}

func (s *TransactionService) CreatePaymentMethod(ctx context.Context, arg db.CreatePaymentMethodParams) (db.PaymentMethod, error) {
	return s.transactions.CreatePaymentMethod(ctx, arg)
}

func (s *TransactionService) UpdatePaymentMethod(ctx context.Context, arg db.UpdatePaymentMethodParams, userID uuid.UUID) (db.PaymentMethod, error) {
	method, err := s.transactions.GetPaymentMethod(ctx, arg.ID)
	if err != nil {
		return db.PaymentMethod{}, err
	}
	if method.BudgetPersonID != nil {
		person, err := s.profiles.GetPersonByID(ctx, *method.BudgetPersonID)
		if err != nil {
			return db.PaymentMethod{}, err
		}
		if err := s.assertProfileCollaborator(ctx, person.BudgetProfileID, userID); err != nil {
			return db.PaymentMethod{}, err
		}
	} else if method.UserID == nil || *method.UserID != userID {
		return db.PaymentMethod{}, apperr.Forbidden("access denied")
	}
	return s.transactions.UpdatePaymentMethod(ctx, arg)
}

// MarkTransactionAsPaid confirms payment for a fixed transaction, updating its
// actual amount and date. If the paid amount differs from planned, also updates
// the fixed expense template so future periods carry the corrected planned cost.
func (s *TransactionService) MarkTransactionAsPaid(ctx context.Context, id uuid.UUID, periodID uuid.UUID, paidAmount pgtype.Numeric, paidDate pgtype.Date, userID uuid.UUID) (db.Transaction, error) {
	period, err := s.assertPeriodCollaborator(ctx, periodID, userID)
	if err != nil {
		return db.Transaction{}, err
	}

	// No separate import to observe category/payment method from — a zero
	// observedPayment lets the paid transaction's own (possibly user-edited)
	// fields speak for it instead. See mark_paid.go.
	return markFixedTransactionPaid(ctx, s.transactions, s.fixedExpenses,
		db.MarkTransactionAsPaidParams{
			ID:             id,
			BudgetPeriodID: periodID,
			Amount:         paidAmount,
			PaidDate:       paidDate,
		},
		autoUpdatePlannedAmountFor(ctx, s.profiles, period.BudgetProfileID, "transaction.mark_paid"),
		observedPayment{},
		"transaction.mark_paid",
	)
}

func (s *TransactionService) UnmarkTransactionAsPaid(ctx context.Context, id uuid.UUID, periodID uuid.UUID, userID uuid.UUID) (db.Transaction, error) {
	if _, err := s.assertPeriodCollaborator(ctx, periodID, userID); err != nil {
		return db.Transaction{}, err
	}
	tx, err := s.transactions.UnmarkAsPaid(ctx, db.UnmarkTransactionAsPaidParams{
		ID:             id,
		BudgetPeriodID: periodID,
	})
	if err != nil {
		return db.Transaction{}, err
	}

	// If this transaction was a confirmed review's match target, undo the
	// confirmation: reset the review to pending and un-exclude the imported
	// variable transaction from totals, restoring it to its normal
	// awaiting-review state. Applies to any Fixed-type transaction —
	// fixed-expense-spawned or savings-derived.
	review, rErr := s.reviews.GetConfirmedByMatchedTransaction(ctx, tx.ID)
	if rErr == nil {
		if varTx, txErr := s.transactions.GetByID(ctx, review.TransactionID); txErr == nil && varTx.Name != nil && tx.FixedExpenseID != nil {
			if aliasErr := s.reviews.DeleteAlias(ctx, *tx.FixedExpenseID, *varTx.Name); aliasErr != nil {
				log.Printf("transaction.unmark_paid: drop alias %q for fixed expense %s: %v", *varTx.Name, *tx.FixedExpenseID, aliasErr)
			}
		}
		if resetErr := s.reviews.ResetByMatchedTransaction(ctx, tx.ID); resetErr != nil {
			// Leaves the review confirmed against a bill that is no longer paid —
			// the inconsistency this whole undo path exists to prevent, so it must
			// not pass silently.
			log.Printf("transaction.unmark_paid: reset review for transaction %s: %v", tx.ID, resetErr)
		}
		if _, excludeErr := s.transactions.SetExcluded(ctx, db.SetTransactionExcludedParams{
			ID:             review.TransactionID,
			BudgetPeriodID: review.BudgetPeriodID,
			Excluded:       false,
		}); excludeErr != nil {
			// The import stays excluded from totals while no longer being
			// matched to anything — money the user spent that counts nowhere.
			log.Printf("transaction.unmark_paid: un-exclude imported transaction %s: %v",
				review.TransactionID, excludeErr)
		}
	}

	return tx, nil
}

// SetTransactionExcluded toggles whether a transaction counts toward totals
// without deleting it (e.g. reimbursements, transfers, or anything else that
// would otherwise disrupt the spending total).
func (s *TransactionService) SetTransactionExcluded(ctx context.Context, id uuid.UUID, periodID uuid.UUID, excluded bool, userID uuid.UUID) (db.Transaction, error) {
	if _, err := s.assertPeriodCollaborator(ctx, periodID, userID); err != nil {
		return db.Transaction{}, err
	}
	return s.transactions.SetExcluded(ctx, db.SetTransactionExcludedParams{
		ID:             id,
		BudgetPeriodID: periodID,
		Excluded:       excluded,
	})
}

func (s *TransactionService) DeletePaymentMethod(ctx context.Context, id, replacementID, budgetProfileID, userID uuid.UUID) error {
	if err := s.assertProfileCollaborator(ctx, budgetProfileID, userID); err != nil {
		return err
	}
	// Verify both methods belong to the given budget profile.
	if _, err := s.transactions.GetPaymentMethod(ctx, id); err != nil {
		return err
	}
	if _, err := s.transactions.GetPaymentMethod(ctx, replacementID); err != nil {
		return err
	}
	return s.transactions.DeletePaymentMethodAndReassign(ctx, db.DeletePaymentMethodAndReassignParams{
		ID:              id,
		ReplacementID:   replacementID,
		BudgetProfileID: budgetProfileID,
	})
}

// ── Transaction review ────────────────────────────────────────────────────────

func (s *TransactionService) ListTransactionReviews(ctx context.Context, userID, profileID uuid.UUID) ([]db.ListTransactionReviewsRow, error) {
	if err := s.assertProfileMember(ctx, profileID, userID); err != nil {
		return nil, err
	}
	return s.reviews.List(ctx, profileID)
}

// MarkTransactionForReview flags a variable transaction as a likely duplicate
// of matchedTransactionID — any Fixed-type transaction in the same period,
// whether spawned from a FixedExpense template or a SavingsSource. Matching
// against the transaction directly (rather than a FixedExpense template)
// means any Fixed-type transaction can be a match target with no separate
// savings-specific path.
func (s *TransactionService) MarkTransactionForReview(ctx context.Context, userID, txID, matchedTransactionID, profileID uuid.UUID) (db.TransactionReview, error) {
	if err := s.assertProfileCollaborator(ctx, profileID, userID); err != nil {
		return db.TransactionReview{}, err
	}
	tx, err := s.transactions.GetByID(ctx, txID)
	if err != nil {
		return db.TransactionReview{}, err
	}
	if tx.TransactionTypeID == nil || *tx.TransactionTypeID != 2 {
		return db.TransactionReview{}, apperr.Invalid("only variable transactions can be flagged for review")
	}
	if tx.BudgetPeriodID == nil {
		return db.TransactionReview{}, apperr.Invalid("transaction has no budget period")
	}
	period, err := s.profiles.GetPeriodByID(ctx, *tx.BudgetPeriodID)
	if err != nil || period.BudgetProfileID != profileID {
		return db.TransactionReview{}, apperr.Forbidden("transaction belongs to a different budget")
	}
	matched, err := s.transactions.GetByID(ctx, matchedTransactionID)
	if err != nil {
		return db.TransactionReview{}, err
	}
	if matched.TransactionTypeID == nil || *matched.TransactionTypeID != 1 {
		return db.TransactionReview{}, apperr.Invalid("can only match against a fixed transaction")
	}
	if matched.BudgetPeriodID == nil || *matched.BudgetPeriodID != *tx.BudgetPeriodID {
		return db.TransactionReview{}, apperr.Forbidden("matched transaction belongs to a different budget")
	}
	review, err := s.reviews.Upsert(ctx, *tx.BudgetPeriodID, txID, matchedTransactionID, 100.0)
	if err != nil {
		return db.TransactionReview{}, err
	}
	// Manual flags used to skip notifying subscribers entirely.
	if s.notifs != nil && tx.Name != nil {
		s.notifs.HandleReviewPending(ctx, profileID, *tx.Name)
	}
	return review, nil
}

func (s *TransactionService) ConfirmTransactionReview(ctx context.Context, userID, reviewID, budgetProfileID uuid.UUID) error {
	review, err := s.reviews.GetByID(ctx, reviewID)
	if err != nil {
		return err
	}
	// Takes the period rather than assertPeriodMember's bare error, since the
	// budget's auto_update_planned_amount setting is resolved from it below.
	period, _, err := s.getUserRoleForPeriod(ctx, review.BudgetPeriodID, userID)
	if err != nil {
		return err
	}

	matchedTx, mErr := s.transactions.GetByID(ctx, review.MatchedTransactionID)
	if mErr == nil {
		importedTx, importedTxErr := s.transactions.GetByID(ctx, review.TransactionID)

		// Save alias so future Plaid imports of the same merchant name
		// auto-confirm — only meaningful when the match target was spawned
		// from a FixedExpense template; savings-derived transactions have no
		// template to alias against.
		if importedTxErr == nil && matchedTx.FixedExpenseID != nil && importedTx.Name != nil {
			if aliasErr := s.reviews.CreateAlias(ctx, *matchedTx.FixedExpenseID, *importedTx.Name); aliasErr != nil {
				// Not fatal: the alias only speeds up *future* imports of this
				// merchant. This confirmation still stands.
				log.Printf("transaction.confirm_review: save alias %q for fixed expense %s: %v",
					*importedTx.Name, *matchedTx.FixedExpenseID, aliasErr)
			}
		}

		// Mark the matched transaction paid if it isn't already. Use the
		// imported variable transaction's actual amount and date (what was
		// really charged, and when it really cleared) rather than the fixed
		// transaction's own planned amount/scheduled date, so the overview
		// reflects the true spend and the template (below) syncs to reality
		// instead of just echoing back what it already had.
		if !matchedTx.IsPaid && matchedTx.BudgetPeriodID != nil {
			paidAmount := matchedTx.PlannedAmount
			paidDate := matchedTx.Date
			var observed observedPayment
			if importedTxErr == nil {
				paidAmount = importedTx.Amount
				paidDate = importedTx.Date
				observed = observedPayment{CategoryID: importedTx.CategoryID, PaymentMethodID: importedTx.PaymentMethodID}
			}
			// This error used to be discarded outright, so a bill that failed
			// to be marked paid was indistinguishable from one that succeeded,
			// and the RPC still reported success to the caller.
			if _, paidErr := markFixedTransactionPaid(ctx, s.transactions, s.fixedExpenses,
				db.MarkTransactionAsPaidParams{
					ID:             matchedTx.ID,
					BudgetPeriodID: *matchedTx.BudgetPeriodID,
					Amount:         paidAmount,
					PaidDate:       paidDate,
				},
				autoUpdatePlannedAmountFor(ctx, s.profiles, period.BudgetProfileID, "transaction.confirm_review"),
				observed,
				"transaction.confirm_review",
			); paidErr != nil {
				// Fatal here, unlike the alias above: confirming a review whose
				// whole point is "this bill was paid" must not report success
				// when the bill is still unpaid. Nothing has been excluded and
				// the review is still pending, so the user can retry.
				return paidErr
			}
		}
	}

	// Exclude the imported variable transaction from totals — same mechanism
	// as an Income transaction — instead of hiding it from ListTransactions
	// entirely. It stays visible and toggleable, so unmarking the matched
	// fixed expense later (which resets this review to "pending") never
	// leaves it stranded behind a review-status side channel.
	if _, excludeErr := s.transactions.SetExcluded(ctx, db.SetTransactionExcludedParams{
		ID:             review.TransactionID,
		BudgetPeriodID: review.BudgetPeriodID,
		Excluded:       true,
	}); excludeErr != nil {
		// Not fatal: the bill is paid and the link is about to be recorded, so
		// the match holds. The consequence is a duplicate still counting toward
		// totals, which the user can toggle off — visible and recoverable,
		// unlike the silent version this replaces.
		log.Printf("transaction.confirm_review: exclude imported transaction %s: %v",
			review.TransactionID, excludeErr)
	}

	if statusErr := s.reviews.UpdateStatus(ctx, reviewID, "confirmed"); statusErr != nil {
		log.Printf("transaction.confirm_review: confirm review %s: %v", reviewID, statusErr)
		return statusErr
	}
	return nil
}

func (s *TransactionService) DismissTransactionReview(ctx context.Context, userID, reviewID uuid.UUID) error {
	review, err := s.reviews.GetByID(ctx, reviewID)
	if err != nil {
		return err
	}
	if err := s.assertPeriodMember(ctx, review.BudgetPeriodID, userID); err != nil {
		return err
	}
	return s.reviews.UpdateStatus(ctx, reviewID, "dismissed")
}
