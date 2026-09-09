package service

import (
	"context"
	"errors"
	"testing"
	"time"

	db "github.com/BeWellSpent/wellspent-backend/internal/sqlc"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The rule these cover: marking a fixed expense paid brings its FixedExpense
// template up to what actually happened — amount, due date, category and
// payment method — but only when the budget opts in
// (BudgetProfile.auto_update_planned_amount, legacy name notwithstanding).
//
// Before this helper existed the three (now four) callers disagreed — only
// the manual button rewrote the template at all, and only its amount.

func TestMarkFixedTransactionPaid_AutoUpdateOn_RewritesTheTemplate(t *testing.T) {
	txID, feID := uuid.New(), uuid.New()
	paid := numericFromNanos(dollars(67.82))
	paidOn := time.Date(2026, time.September, 12, 0, 0, 0, 0, time.UTC) // a Saturday
	var updated *db.UpdateFixedExpenseFromPaymentParams

	tx, err := markFixedTransactionPaid(context.Background(),
		&mockTransactionRepo{
			markAsPaid: func(_ context.Context, arg db.MarkTransactionAsPaidParams) (db.Transaction, error) {
				return db.Transaction{ID: arg.ID, FixedExpenseID: &feID}, nil
			},
		},
		&mockFixedExpenseRepo{
			getByID: func(_ context.Context, id uuid.UUID) (db.FixedExpense, error) {
				return db.FixedExpense{ID: id, DayOfMonth: 1}, nil
			},
			updateFromPayment: func(_ context.Context, arg db.UpdateFixedExpenseFromPaymentParams) error {
				updated = &arg
				return nil
			},
		},
		db.MarkTransactionAsPaidParams{ID: txID, Amount: paid, PaidDate: pgtype.Date{Time: paidOn, Valid: true}},
		true,
		observedPayment{},
		"test",
	)

	require.NoError(t, err)
	assert.Equal(t, txID, tx.ID)
	require.NotNil(t, updated, "the template should follow what actually happened")
	assert.Equal(t, feID, updated.ID)
	assert.Equal(t, dollars(67.82), numericToNanos(updated.PlannedAmount))
	assert.Equal(t, int32(12), updated.DayOfMonth, "day should follow the real paid date, not the original due day")
	assert.Equal(t, int16(6), updated.DayOfWeek, "Saturday is ISO weekday 6")
	assert.True(t, updated.AnchorDate.Valid)
	assert.Equal(t, paidOn, updated.AnchorDate.Time)
}

func TestMarkFixedTransactionPaid_AutoUpdateOff_LeavesTheTemplateAlone(t *testing.T) {
	feID := uuid.New()
	called := false

	_, err := markFixedTransactionPaid(context.Background(),
		&mockTransactionRepo{
			markAsPaid: func(_ context.Context, arg db.MarkTransactionAsPaidParams) (db.Transaction, error) {
				return db.Transaction{ID: arg.ID, FixedExpenseID: &feID}, nil
			},
		},
		&mockFixedExpenseRepo{
			updateFromPayment: func(_ context.Context, _ db.UpdateFixedExpenseFromPaymentParams) error {
				called = true
				return nil
			},
		},
		db.MarkTransactionAsPaidParams{ID: uuid.New(), Amount: numericFromNanos(dollars(67.82))},
		false,
		observedPayment{},
		"test",
	)

	require.NoError(t, err)
	assert.False(t, called, "the plan must stay put when the budget hasn't opted in")
}

// A one-off Fixed transaction with no template behind it has nothing to update.
func TestMarkFixedTransactionPaid_NoTemplate_SkipsTheUpdate(t *testing.T) {
	called := false

	_, err := markFixedTransactionPaid(context.Background(),
		&mockTransactionRepo{
			markAsPaid: func(_ context.Context, arg db.MarkTransactionAsPaidParams) (db.Transaction, error) {
				return db.Transaction{ID: arg.ID}, nil // FixedExpenseID nil
			},
		},
		&mockFixedExpenseRepo{
			updateFromPayment: func(_ context.Context, _ db.UpdateFixedExpenseFromPaymentParams) error {
				called = true
				return nil
			},
		},
		db.MarkTransactionAsPaidParams{ID: uuid.New()},
		true,
		observedPayment{},
		"test",
	)

	require.NoError(t, err)
	assert.False(t, called)
}

// The payment is what matters; a failed template write is a wrong plan, not a
// wrong payment, and must not cost the caller a recorded payment.
func TestMarkFixedTransactionPaid_TemplateUpdateFails_PaymentStillStands(t *testing.T) {
	feID := uuid.New()

	tx, err := markFixedTransactionPaid(context.Background(),
		&mockTransactionRepo{
			markAsPaid: func(_ context.Context, arg db.MarkTransactionAsPaidParams) (db.Transaction, error) {
				return db.Transaction{ID: arg.ID, FixedExpenseID: &feID}, nil
			},
		},
		&mockFixedExpenseRepo{
			updateFromPayment: func(_ context.Context, _ db.UpdateFixedExpenseFromPaymentParams) error {
				return errors.New("boom")
			},
		},
		db.MarkTransactionAsPaidParams{ID: uuid.New()},
		true,
		observedPayment{},
		"test",
	)

	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, tx.ID)
}

// Same non-fatal posture when reading the template back fails — nothing to
// sync against, so the update is skipped, but the payment already landed.
func TestMarkFixedTransactionPaid_ReadingTemplateFails_PaymentStillStands(t *testing.T) {
	feID := uuid.New()
	called := false

	tx, err := markFixedTransactionPaid(context.Background(),
		&mockTransactionRepo{
			markAsPaid: func(_ context.Context, arg db.MarkTransactionAsPaidParams) (db.Transaction, error) {
				return db.Transaction{ID: arg.ID, FixedExpenseID: &feID}, nil
			},
		},
		&mockFixedExpenseRepo{
			getByID: func(_ context.Context, _ uuid.UUID) (db.FixedExpense, error) {
				return db.FixedExpense{}, errors.New("gone")
			},
			updateFromPayment: func(_ context.Context, _ db.UpdateFixedExpenseFromPaymentParams) error {
				called = true
				return nil
			},
		},
		db.MarkTransactionAsPaidParams{ID: uuid.New()},
		true,
		observedPayment{},
		"test",
	)

	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, tx.ID)
	assert.False(t, called)
}

// Marking paid failing IS fatal — it used to be discarded with `_, _ =` in
// ConfirmTransactionReview, so a bill that never got marked paid was
// indistinguishable from one that did.
func TestMarkFixedTransactionPaid_MarkFails_ReturnsTheError(t *testing.T) {
	_, err := markFixedTransactionPaid(context.Background(),
		&mockTransactionRepo{
			markAsPaid: func(_ context.Context, _ db.MarkTransactionAsPaidParams) (db.Transaction, error) {
				return db.Transaction{}, errors.New("db down")
			},
		},
		&mockFixedExpenseRepo{},
		db.MarkTransactionAsPaidParams{ID: uuid.New()},
		true,
		observedPayment{},
		"test",
	)

	require.Error(t, err)
}

// Confirming a Plaid match: the category/payment method actually observed on
// the imported transaction should win, even though it differs from what the
// spawned fixed transaction itself carries (the template's stale values).
func TestMarkFixedTransactionPaid_ObservedCategoryAndPaymentMethod_OverrideTheTemplate(t *testing.T) {
	feID := uuid.New()
	staleCategoryID := int32(1)
	stalePMID := uuid.New()
	observedCategoryID := int32(2)
	observedPMID := uuid.New()
	var updated *db.UpdateFixedExpenseFromPaymentParams

	_, err := markFixedTransactionPaid(context.Background(),
		&mockTransactionRepo{
			markAsPaid: func(_ context.Context, arg db.MarkTransactionAsPaidParams) (db.Transaction, error) {
				// The paid transaction is the spawned fixed one — still
				// carrying the template's old category/payment method.
				return db.Transaction{ID: arg.ID, FixedExpenseID: &feID, CategoryID: &staleCategoryID, PaymentMethodID: &stalePMID}, nil
			},
		},
		&mockFixedExpenseRepo{
			getByID: func(_ context.Context, id uuid.UUID) (db.FixedExpense, error) {
				return db.FixedExpense{ID: id, CategoryID: &staleCategoryID, PaymentMethodID: &stalePMID}, nil
			},
			updateFromPayment: func(_ context.Context, arg db.UpdateFixedExpenseFromPaymentParams) error {
				updated = &arg
				return nil
			},
		},
		db.MarkTransactionAsPaidParams{ID: uuid.New()},
		true,
		observedPayment{CategoryID: &observedCategoryID, PaymentMethodID: &observedPMID},
		"test",
	)

	require.NoError(t, err)
	require.NotNil(t, updated)
	require.NotNil(t, updated.CategoryID)
	assert.Equal(t, observedCategoryID, *updated.CategoryID)
	require.NotNil(t, updated.PaymentMethodID)
	assert.Equal(t, observedPMID, *updated.PaymentMethodID)
}

// The plain manual button has no import to observe from — the paid
// transaction's own (possibly user-edited) fields are the next best source.
func TestMarkFixedTransactionPaid_NoObservation_FallsBackToThePaidTransactionsOwnFields(t *testing.T) {
	feID := uuid.New()
	templateCategoryID := int32(1)
	editedCategoryID := int32(2)
	var updated *db.UpdateFixedExpenseFromPaymentParams

	_, err := markFixedTransactionPaid(context.Background(),
		&mockTransactionRepo{
			markAsPaid: func(_ context.Context, arg db.MarkTransactionAsPaidParams) (db.Transaction, error) {
				return db.Transaction{ID: arg.ID, FixedExpenseID: &feID, CategoryID: &editedCategoryID}, nil
			},
		},
		&mockFixedExpenseRepo{
			getByID: func(_ context.Context, id uuid.UUID) (db.FixedExpense, error) {
				return db.FixedExpense{ID: id, CategoryID: &templateCategoryID}, nil
			},
			updateFromPayment: func(_ context.Context, arg db.UpdateFixedExpenseFromPaymentParams) error {
				updated = &arg
				return nil
			},
		},
		db.MarkTransactionAsPaidParams{ID: uuid.New()},
		true,
		observedPayment{}, // no import to observe (the plain button path)
		"test",
	)

	require.NoError(t, err)
	require.NotNil(t, updated)
	require.NotNil(t, updated.CategoryID)
	assert.Equal(t, editedCategoryID, *updated.CategoryID, "the paid transaction's own edited category should win")
}

// Neither the observation nor the paid transaction has anything to say about
// category/payment method — the template must keep its own value rather than
// being cleared to nil.
func TestMarkFixedTransactionPaid_NothingObserved_KeepsTheTemplatesExistingValues(t *testing.T) {
	feID := uuid.New()
	templateCategoryID := int32(3)
	templatePMID := uuid.New()
	var updated *db.UpdateFixedExpenseFromPaymentParams

	_, err := markFixedTransactionPaid(context.Background(),
		&mockTransactionRepo{
			markAsPaid: func(_ context.Context, arg db.MarkTransactionAsPaidParams) (db.Transaction, error) {
				return db.Transaction{ID: arg.ID, FixedExpenseID: &feID}, nil // no category/payment method at all
			},
		},
		&mockFixedExpenseRepo{
			getByID: func(_ context.Context, id uuid.UUID) (db.FixedExpense, error) {
				return db.FixedExpense{ID: id, CategoryID: &templateCategoryID, PaymentMethodID: &templatePMID}, nil
			},
			updateFromPayment: func(_ context.Context, arg db.UpdateFixedExpenseFromPaymentParams) error {
				updated = &arg
				return nil
			},
		},
		db.MarkTransactionAsPaidParams{ID: uuid.New()},
		true,
		observedPayment{},
		"test",
	)

	require.NoError(t, err)
	require.NotNil(t, updated)
	require.NotNil(t, updated.CategoryID)
	assert.Equal(t, templateCategoryID, *updated.CategoryID)
	require.NotNil(t, updated.PaymentMethodID)
	assert.Equal(t, templatePMID, *updated.PaymentMethodID)
}

// Defensive: every real caller supplies a valid PaidDate, but if one somehow
// didn't, the template's existing schedule must survive rather than being
// overwritten with zero-time garbage.
func TestMarkFixedTransactionPaid_InvalidPaidDate_KeepsTheTemplatesExistingSchedule(t *testing.T) {
	feID := uuid.New()
	existingAnchor := pgtype.Date{Time: time.Date(2026, time.March, 1, 0, 0, 0, 0, time.UTC), Valid: true}
	var updated *db.UpdateFixedExpenseFromPaymentParams

	_, err := markFixedTransactionPaid(context.Background(),
		&mockTransactionRepo{
			markAsPaid: func(_ context.Context, arg db.MarkTransactionAsPaidParams) (db.Transaction, error) {
				return db.Transaction{ID: arg.ID, FixedExpenseID: &feID}, nil
			},
		},
		&mockFixedExpenseRepo{
			getByID: func(_ context.Context, id uuid.UUID) (db.FixedExpense, error) {
				return db.FixedExpense{ID: id, DayOfMonth: 1, DayOfWeek: 7, AnchorDate: existingAnchor}, nil
			},
			updateFromPayment: func(_ context.Context, arg db.UpdateFixedExpenseFromPaymentParams) error {
				updated = &arg
				return nil
			},
		},
		db.MarkTransactionAsPaidParams{ID: uuid.New()}, // PaidDate left zero (Valid: false)
		true,
		observedPayment{},
		"test",
	)

	require.NoError(t, err)
	require.NotNil(t, updated)
	assert.Equal(t, int32(1), updated.DayOfMonth)
	assert.Equal(t, int16(7), updated.DayOfWeek)
	assert.Equal(t, existingAnchor, updated.AnchorDate)
}

// A profile that can't be read must not silently flip the behaviour: the column
// defaults to true, and so does the fallback.
func TestAutoUpdatePlannedAmountFor_UnreadableProfileDefaultsToTrue(t *testing.T) {
	got := autoUpdatePlannedAmountFor(context.Background(),
		&mockBudgetProfileRepo{
			getByID: func(_ context.Context, _ uuid.UUID) (db.BudgetProfile, error) {
				return db.BudgetProfile{}, errors.New("gone")
			},
		}, uuid.New(), "test")

	assert.True(t, got)
}

func TestAutoUpdatePlannedAmountFor_ReadsTheProfileSetting(t *testing.T) {
	got := autoUpdatePlannedAmountFor(context.Background(),
		&mockBudgetProfileRepo{
			getByID: func(_ context.Context, _ uuid.UUID) (db.BudgetProfile, error) {
				return db.BudgetProfile{AutoUpdatePlannedAmount: false}, nil
			},
		}, uuid.New(), "test")

	assert.False(t, got)
}
