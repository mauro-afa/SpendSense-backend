package service

import (
	"context"
	"log"

	"github.com/BeWellSpent/wellspent-backend/internal/repository"
	db "github.com/BeWellSpent/wellspent-backend/internal/sqlc"
	"github.com/google/uuid"
)

// Marking a fixed transaction paid, in one place.
//
// Three call sites do this: the MarkTransactionAsPaid RPC (the button),
// ConfirmTransactionReview (the To Review tab), and the Plaid sync's
// auto-confirm branch. They used to disagree — only the button rewrote the
// FixedExpense template when the paid amount differed from the plan — so the
// same bill paid three different ways left the next period planned at two
// different figures.
//
// Whether the template follows reality is a budgeting judgement, so it stays
// BudgetProfile.auto_update_planned_amount, decided by the budget owner and
// honoured identically by all three. The setting's name is legacy — it now
// governs four fields, not just the amount:
//   - planned_amount: what was actually paid
//   - day_of_month / day_of_week / anchor_date: the day it was actually paid,
//     not the day it was originally due
//   - category_id: the category actually observed on the real transaction,
//     when there was one to observe (a Plaid import)
//   - payment_method_id: same, for payment method
//
// Note what does NOT change: the period being paid keeps its own
// planned_amount/date. Only the template moves, so only future periods are
// affected — which is what lets a client show "$90 planned, $67 paid"
// alongside a marker saying the $67 applies from next period on. A payment a
// few days late within the same month/week does not disturb which month/week
// a longer-interval (e.g. quarterly) bill is next due in, since that math
// keys off the anchor's month/week, not its day — but a payment recorded on
// the other side of a month or week boundary from when it was due can shift
// that. Accepted as a narrow, known edge case rather than solved for here.

// observedPayment carries what was actually observed about a payment, for the
// two fields where "the transaction being marked paid" and "the real-world
// transaction the payment came from" can differ: confirming a Plaid match
// marks the *fixed* (spawned) transaction paid, but its category/payment
// method may not be what Plaid actually saw on the *imported* one — that's
// the observation worth propagating. A nil field means "nothing to observe"
// — the template keeps its existing value rather than being overwritten with
// a guess. The plain manual button has no separate import to observe from,
// so it always passes a zero value and lets the paid transaction's own
// (possibly user-edited) fields speak for it instead — see below.
type observedPayment struct {
	CategoryID      *int32
	PaymentMethodID *uuid.UUID
}

// markFixedTransactionPaid marks a transaction paid and, when the budget opts
// in, brings its FixedExpense template up to date with what actually
// happened — see the package doc comment above for exactly which fields.
//
// Every failure is logged with the transaction it concerns. These used to be
// discarded — `_, _ =` on the mark itself in ConfirmTransactionReview, `_ =` on
// the template update — which is how a bill could end up paid for reasons
// nothing recorded, diagnosable only from the database.
func markFixedTransactionPaid(
	ctx context.Context,
	transactions repository.TransactionRepository,
	fixedExpenses repository.FixedExpenseRepository,
	arg db.MarkTransactionAsPaidParams,
	autoSyncTemplate bool,
	observed observedPayment,
	caller string,
) (db.Transaction, error) {
	tx, err := transactions.MarkAsPaid(ctx, arg)
	if err != nil {
		log.Printf("%s: mark transaction %s paid: %v", caller, arg.ID, err)
		return db.Transaction{}, err
	}

	if !autoSyncTemplate || tx.FixedExpenseID == nil {
		return tx, nil
	}

	fe, feErr := fixedExpenses.GetByID(ctx, *tx.FixedExpenseID)
	if feErr != nil {
		// Not fatal: the payment is recorded and correct. Only the template
		// missed the update, so next period keeps the old plan/date/category/
		// payment method — a wrong template, not a wrong payment, and the
		// user can edit it by hand.
		log.Printf("%s: transaction %s paid, but reading fixed expense %s to sync it failed: %v",
			caller, tx.ID, *tx.FixedExpenseID, feErr)
		return tx, nil
	}

	// Category/payment method: prefer what was actually observed (the
	// imported transaction, for a Plaid match); otherwise fall back to
	// whatever the just-paid transaction itself carries (correct for the
	// plain button, where there is no separate import); otherwise leave the
	// template exactly as it already is, rather than clearing a real value
	// with a nil one.
	categoryID := fe.CategoryID
	if observed.CategoryID != nil {
		categoryID = observed.CategoryID
	} else if tx.CategoryID != nil {
		categoryID = tx.CategoryID
	}
	paymentMethodID := fe.PaymentMethodID
	if observed.PaymentMethodID != nil {
		paymentMethodID = observed.PaymentMethodID
	} else if tx.PaymentMethodID != nil {
		paymentMethodID = tx.PaymentMethodID
	}

	// Due date: derive from the real paid date. Left as the template's
	// existing schedule if PaidDate somehow isn't set — every real caller
	// supplies one (the RPC requires paid_at; the other two source it from an
	// existing transaction row), so this is a defensive fallback, not an
	// expected path.
	dayOfMonth, dayOfWeek, anchorDate := fe.DayOfMonth, fe.DayOfWeek, fe.AnchorDate
	if arg.PaidDate.Valid {
		var week int32
		dayOfMonth, week, anchorDate = fixedExpenseScheduleFromAnchor(arg.PaidDate.Time)
		dayOfWeek = int16(week)
	}

	// Deliberately unconditional on any single field actually differing:
	// writing the same value back is harmless, and comparing pgtype.Numeric/
	// pgtype.Date here would be more places to get equality subtly wrong.
	if updateErr := fixedExpenses.UpdateFromPayment(ctx, db.UpdateFixedExpenseFromPaymentParams{
		ID:              fe.ID,
		PlannedAmount:   arg.Amount,
		DayOfMonth:      dayOfMonth,
		DayOfWeek:       dayOfWeek,
		AnchorDate:      anchorDate,
		CategoryID:      categoryID,
		PaymentMethodID: paymentMethodID,
	}); updateErr != nil {
		log.Printf("%s: transaction %s paid, but syncing fixed expense %s failed: %v",
			caller, tx.ID, fe.ID, updateErr)
	}
	return tx, nil
}

// autoUpdatePlannedAmountFor resolves the budget's setting from a period. The
// name is legacy — see markFixedTransactionPaid's doc comment for the full
// set of fields this now gates.
//
// Defaults to true when the profile can't be read, matching both the column
// default and the behaviour every path had before the setting existed: a
// failed lookup must not silently change what marking a bill paid does.
func autoUpdatePlannedAmountFor(
	ctx context.Context,
	profiles repository.BudgetProfileRepository,
	profileID uuid.UUID,
	caller string,
) bool {
	profile, err := profiles.GetByID(ctx, profileID)
	if err != nil {
		log.Printf("%s: read auto_update_planned_amount for profile %s: %v", caller, profileID, err)
		return true
	}
	return profile.AutoUpdatePlannedAmount
}
