package service

import (
	"context"
	"errors"
	"github.com/BeWellSpent/wellspent-backend/internal/category"
	"testing"
	"time"

	"github.com/BeWellSpent/wellspent-backend/internal/apperr"
	"github.com/BeWellSpent/wellspent-backend/internal/crypto"
	plaidclient "github.com/BeWellSpent/wellspent-backend/internal/plaid"
	db "github.com/BeWellSpent/wellspent-backend/internal/sqlc"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func numericFromString(t *testing.T, s string) pgtype.Numeric {
	t.Helper()
	var n pgtype.Numeric
	require.NoError(t, n.Scan(s))
	return n
}

func numericToString(t *testing.T, n pgtype.Numeric) string {
	t.Helper()
	v, err := n.Value()
	require.NoError(t, err)
	s, ok := v.(string)
	require.True(t, ok, "pgtype.Numeric.Value() is documented to return a string")
	return s
}

func makeFixedExpense(t *testing.T, id uuid.UUID, name, amount string) db.FixedExpense {
	t.Helper()
	return db.FixedExpense{
		ID:            id,
		Name:          name,
		PlannedAmount: numericFromString(t, amount),
	}
}

func TestSyncScoreBestMatch_AliasHitPicksAmountCompatibleCandidate(t *testing.T) {
	fe15ID := uuid.New()
	fe5ID := uuid.New()
	fe15 := makeFixedExpense(t, fe15ID, "Creator Support A", "15.00")
	fe5 := makeFixedExpense(t, fe5ID, "Creator Support B", "5.33")
	aliases := map[uuid.UUID][]string{
		fe15ID: {"Patreon"},
		fe5ID:  {"Patreon"},
	}

	tx := plaidclient.Transaction{Name: "Patreon", Amount: 5.33}
	score, bestFE, aliasHit, amountOK := syncScoreBestMatch(tx, nil, nil, []db.FixedExpense{fe15, fe5}, aliases)

	require.NotNil(t, bestFE)
	assert.Equal(t, fe5ID, bestFE.ID, "should resolve to the amount-matching template, not just the first alias hit")
	assert.True(t, aliasHit)
	assert.True(t, amountOK)
	assert.Equal(t, 60.0, score)
}

func TestSyncScoreBestMatch_AliasHitWithoutAmountMatchDoesNotReachAutoConfirmThreshold(t *testing.T) {
	feID := uuid.New()
	fe := makeFixedExpense(t, feID, "Creator Support", "15.00")
	aliases := map[uuid.UUID][]string{feID: {"Patreon"}}

	tx := plaidclient.Transaction{Name: "Patreon", Amount: 5.33}
	score, bestFE, aliasHit, amountOK := syncScoreBestMatch(tx, nil, nil, []db.FixedExpense{fe}, aliases)

	require.NotNil(t, bestFE)
	assert.True(t, aliasHit)
	assert.False(t, amountOK)
	assert.Less(t, score, 80.0)
}

func TestSyncScoreBestMatch_FallsBackToWordOverlapWithoutAlias(t *testing.T) {
	feID := uuid.New()
	fe := makeFixedExpense(t, feID, "Amex Renewal Membership", "150.00")
	tx := plaidclient.Transaction{Name: "RENEWAL MEMBERSHIP FEE", Amount: 150.00}

	score, bestFE, aliasHit, amountOK := syncScoreBestMatch(tx, nil, nil, []db.FixedExpense{fe}, nil)

	require.NotNil(t, bestFE)
	assert.False(t, aliasHit)
	assert.True(t, amountOK)
	assert.Equal(t, 60.0, score)
}

// The reported case: a saved alias carrying a bank-supplied date/reference
// number ("Manual DB-Bkrg 09/02") must still hit next month, when the same
// merchant's real descriptor comes in with a different date ("10/02") — an
// exact-string alias match would fail this every time, forever, since the
// date never repeats.
func TestSyncScoreBestMatch_AliasHitSurvivesADifferentTrailingDate(t *testing.T) {
	feID := uuid.New()
	fe := makeFixedExpense(t, feID, "Emma - broker", "120.00")
	aliases := map[uuid.UUID][]string{feID: {"Manual DB-Bkrg 09/02"}}

	tx := plaidclient.Transaction{Name: "Manual DB-Bkrg 10/02", Amount: 120.00}
	score, bestFE, aliasHit, amountOK := syncScoreBestMatch(tx, nil, nil, []db.FixedExpense{fe}, aliases)

	require.NotNil(t, bestFE)
	assert.Equal(t, feID, bestFE.ID)
	assert.True(t, aliasHit, "the alias should still hit via word overlap once the date is stripped")
	assert.True(t, amountOK)
	assert.Equal(t, 60.0, score)
}

// An alias for one fixed expense must not cross-match a transaction meant for
// another just because both happen to be generic bank boilerplate with no
// real shared word.
func TestSyncScoreBestMatch_AliasDoesNotMatchWhenNoWordsAreShared(t *testing.T) {
	feID := uuid.New()
	fe := makeFixedExpense(t, feID, "Emma - broker", "120.00")
	aliases := map[uuid.UUID][]string{feID: {"Manual DB-Bkrg 09/02"}}

	tx := plaidclient.Transaction{Name: "Zelle Payment To John 10/02", Amount: 120.00}
	_, _, aliasHit, _ := syncScoreBestMatch(tx, nil, nil, []db.FixedExpense{fe}, aliases)

	assert.False(t, aliasHit)
}

func TestSyncScoreBestMatch_NoCandidatesReturnsNil(t *testing.T) {
	tx := plaidclient.Transaction{Name: "Patreon", Amount: 5.33}
	score, bestFE, aliasHit, amountOK := syncScoreBestMatch(tx, nil, nil, nil, nil)

	assert.Nil(t, bestFE)
	assert.Equal(t, 0.0, score)
	assert.False(t, aliasHit)
	assert.False(t, amountOK)
}

func TestSyncAmountWithinTolerance(t *testing.T) {
	feID := uuid.New()
	fe := makeFixedExpense(t, feID, "Rent", "1000.00")

	assert.True(t, syncAmountWithinTolerance(1000.00, &fe))
	assert.True(t, syncAmountWithinTolerance(997.50, &fe), "within $3 tolerance")
	assert.False(t, syncAmountWithinTolerance(990.00, &fe), "outside $3 tolerance")
}

func TestSyncNameWordsOverlap(t *testing.T) {
	assert.True(t, syncNameWordsOverlap("renewal membership fee", "amex renewal"))
	assert.False(t, syncNameWordsOverlap("patreon", "creator support subscription"))
}

func TestSyncResolveCategory_PayrollNameOverridesPFCCategory(t *testing.T) {
	assert.Equal(t, category.Income, syncResolveCategory("ACME CORP PAYROLL", "TRANSFER_IN", "TRANSFER_IN_DEPOSIT"))
	assert.Equal(t, category.Income, syncResolveCategory("payroll deposit", "", ""))
	assert.Equal(t, category.Income, syncResolveCategory("Bi-Weekly Payroll", "GENERAL_MERCHANDISE", "GENERAL_MERCHANDISE_PET_SUPPLIES"))
}

func TestSyncResolveCategory_NonPayrollFallsBackToPFCMapping(t *testing.T) {
	assert.Equal(t, category.Groceries, syncResolveCategory("WHOLE FOODS", "FOOD_AND_DRINK", "FOOD_AND_DRINK_GROCERIES"))
	assert.Equal(t, category.Key(""), syncResolveCategory("UNKNOWN MERCHANT", "", ""))
}

func TestSyncResolveCategory_IncomePFCPrimaryResolvesWithoutPayrollInName(t *testing.T) {
	// A direct-deposit paycheck whose name never says "payroll" (e.g. the
	// employer's legal entity name) must still resolve to Income via Plaid's
	// own personal_finance_category classification, not just the name check.
	assert.Equal(t, category.Income, syncResolveCategory("ACME CORP DIRECT DEP", "INCOME", "INCOME_WAGES"))
	assert.Equal(t, category.Income, syncResolveCategory("IRS TREAS 310 TAX REF", "INCOME", "INCOME_TAX_REFUND"))
}

func TestSyncResolveCategoryID_ResolvesToKnownID(t *testing.T) {
	categoryIDs := map[category.Key]int32{category.Shopping: 7}
	key, id := syncResolveCategoryID("AMAZON.COM", "GENERAL_MERCHANDISE", "GENERAL_MERCHANDISE_ONLINE_MARKETPLACES", categoryIDs)
	assert.Equal(t, category.Shopping, key)
	require.NotNil(t, id)
	assert.Equal(t, int32(7), *id)
}

func TestSyncResolveCategoryID_UnmappedNameReturnsNilID(t *testing.T) {
	// Resolves to Shopping but the system-category map doesn't have it —
	// this is exactly the scenario that silently drops the category: the
	// transaction still imports, just with category_id NULL.
	categoryIDs := map[category.Key]int32{category.Groceries: 3}
	key, id := syncResolveCategoryID("AMAZON.COM", "GENERAL_MERCHANDISE", "GENERAL_MERCHANDISE_ONLINE_MARKETPLACES", categoryIDs)
	assert.Equal(t, category.Shopping, key)
	assert.Nil(t, id)
}

func TestSyncResolveCategoryID_NoResolvedNameReturnsEmpty(t *testing.T) {
	key, id := syncResolveCategoryID("UNKNOWN MERCHANT", "", "", map[category.Key]int32{})
	assert.Equal(t, category.Key(""), key)
	assert.Nil(t, id)
}

func TestSyncCategoryLogValue(t *testing.T) {
	id := int32(7)
	assert.Equal(t, `"shopping"`, syncCategoryLogValue(category.Shopping, &id))
	assert.Equal(t, `"shopping" (unmapped — no matching system category, imported without a category)`, syncCategoryLogValue(category.Shopping, nil))
	assert.Equal(t, "none", syncCategoryLogValue("", nil))
}

func TestSyncItem_ReturnsErrorWhenCursorPersistFails(t *testing.T) {
	itemID := uuid.New()
	profileID := uuid.New()
	encrypted, err := crypto.Encrypt("real-access-token", testEncKey)
	require.NoError(t, err)

	svc := NewPlaidService(
		&mockPlaidClient{
			syncTransactions: func(_ context.Context, _, _ string) ([]plaidclient.Transaction, []plaidclient.Transaction, []string, string, error) {
				return nil, nil, nil, "new-cursor", nil
			},
		},
		&mockPlaidRepo{
			updateSync: func(_ context.Context, _ db.UpdatePlaidItemSyncParams) (db.PlaidItem, error) {
				return db.PlaidItem{}, errors.New("connection reset")
			},
		},
		&mockBudgetProfileRepo{}, // budgets — unreached: SyncTransactions returns no added transactions
		&mockUserRepo{
			getByID: func(_ context.Context, _ uuid.UUID) (db.User, error) {
				return db.User{Plan: "pro"}, nil
			},
		},
		&mockTransactionRepo{},
		&mockFixedExpenseRepo{},
		&mockTransactionReviewRepo{},
		testEncKey,
	)

	item := db.PlaidItem{ID: itemID, BudgetProfileID: profileID, AccessToken: encrypted}
	syncErr := svc.SyncItem(context.Background(), item)

	require.Error(t, syncErr, "a failure to persist the new cursor must surface as a sync failure, not be silently swallowed")
	assert.Contains(t, syncErr.Error(), "persist cursor")
}

func TestSyncItem_AutoConfirmedMatch_ExcludesInsteadOfDeleting(t *testing.T) {
	itemID := uuid.New()
	profileID := uuid.New()
	periodID := uuid.New()
	feID := uuid.New()
	unpaidTxID := uuid.New()
	insertedTxID := uuid.New()
	encrypted, err := crypto.Encrypt("real-access-token", testEncKey)
	require.NoError(t, err)

	var deleteCalled bool
	var excludedID uuid.UUID
	var excludedFlag bool
	var markedPaidID uuid.UUID
	var confirmedReviewID uuid.UUID
	var confirmedStatus string

	svc := NewPlaidService(
		&mockPlaidClient{
			syncTransactions: func(_ context.Context, _, _ string) ([]plaidclient.Transaction, []plaidclient.Transaction, []string, string, error) {
				return []plaidclient.Transaction{
					{PlaidID: "plaid-tx-1", Name: "Patreon", Amount: 15.00, Date: time.Now()},
				}, nil, nil, "new-cursor", nil
			},
		},
		&mockPlaidRepo{},
		&mockBudgetProfileRepo{
			getPeriodByDate: func(_ context.Context, _ uuid.UUID, _ pgtype.Date) (db.BudgetPeriod, error) {
				return db.BudgetPeriod{ID: periodID}, nil
			},
		},
		&mockUserRepo{
			getByID: func(_ context.Context, _ uuid.UUID) (db.User, error) {
				return db.User{Plan: "pro"}, nil
			},
		},
		&mockTransactionRepo{
			createTransactionFromPlaid: func(_ context.Context, _ db.CreateTransactionFromPlaidParams) (db.Transaction, error) {
				return db.Transaction{ID: insertedTxID}, nil
			},
			markAsPaid: func(_ context.Context, arg db.MarkTransactionAsPaidParams) (db.Transaction, error) {
				markedPaidID = arg.ID
				return db.Transaction{ID: arg.ID}, nil
			},
			setExcluded: func(_ context.Context, arg db.SetTransactionExcludedParams) (db.Transaction, error) {
				excludedID = arg.ID
				excludedFlag = arg.Excluded
				return db.Transaction{ID: arg.ID, IsExcluded: arg.Excluded}, nil
			},
			delete: func(_ context.Context, _ db.DeleteTransactionParams) error {
				deleteCalled = true
				return nil
			},
		},
		&mockFixedExpenseRepo{
			list: func(_ context.Context, _ uuid.UUID) ([]db.FixedExpense, error) {
				return []db.FixedExpense{makeFixedExpense(t, feID, "Creator Support", "15.00")}, nil
			},
			getUnpaidTransactionInPer: func(_ context.Context, arg db.GetUnpaidTransactionByFixedExpenseInPeriodParams) (db.Transaction, error) {
				// Scoped to the imported transaction's own period (issue #41).
				if arg.BudgetPeriodID != periodID {
					return db.Transaction{}, apperr.NotFound("transaction", arg.FixedExpenseID.String())
				}
				return db.Transaction{ID: unpaidTxID, BudgetPeriodID: &periodID, PlannedAmount: numericFromString(t, "15.00")}, nil
			},
		},
		&mockTransactionReviewRepo{
			listAliases: func(_ context.Context, _ uuid.UUID) ([]string, error) {
				return []string{"Patreon"}, nil
			},
			create: func(_ context.Context, _, transactionID, matchedTransactionID uuid.UUID, _ float64) (db.TransactionReview, error) {
				reviewID := uuid.New()
				confirmedReviewID = reviewID
				return db.TransactionReview{ID: reviewID, TransactionID: transactionID, MatchedTransactionID: matchedTransactionID}, nil
			},
			updateStatus: func(_ context.Context, id uuid.UUID, status string) error {
				assert.Equal(t, confirmedReviewID, id, "should confirm the review just created for this auto-match")
				confirmedStatus = status
				return nil
			},
		},
		testEncKey,
	)

	item := db.PlaidItem{ID: itemID, BudgetProfileID: profileID, AccessToken: encrypted}
	syncErr := svc.SyncItem(context.Background(), item)
	require.NoError(t, syncErr)

	assert.False(t, deleteCalled, "the auto-matched imported transaction must not be deleted")
	assert.Equal(t, insertedTxID, excludedID, "should exclude the imported transaction, not the matched fixed one")
	assert.True(t, excludedFlag)
	assert.Equal(t, unpaidTxID, markedPaidID, "should mark the matched fixed transaction paid")
	assert.Equal(t, "confirmed", confirmedStatus, "should record the auto-match as a confirmed review so unmarking paid can find and undo it")
}

// TestSyncItem_PendingTransactionSettles_ConfirmedReview_RepointsAndUpdatesPaidAmount
// covers issue #67: a pending transaction that was already matched and
// confirmed against a fixed expense — the bill is already marked paid, off
// the pending amount — later settles under a brand-new Plaid id, linked back
// only via PendingTransactionID. The existing local row must be updated in
// place (never deleted-and-reimported, which would cascade-delete the
// confirmed review), and the settled amount must propagate to the already-
// paid transaction rather than leaving it stuck at the pending figure.
func TestSyncItem_PendingTransactionSettles_ConfirmedReview_RepointsAndUpdatesPaidAmount(t *testing.T) {
	itemID := uuid.New()
	profileID := uuid.New()
	periodID := uuid.New()
	existingTxID := uuid.New()
	matchedTxID := uuid.New()
	const pendingPlaidID = "plaid-tx-pending"
	const settledPlaidID = "plaid-tx-settled"

	var repointArg db.RepointTransactionPlaidIDParams
	var repointCalled bool
	var markPaidArg db.MarkTransactionAsPaidParams
	var markPaidCalled bool

	svc := NewPlaidService(
		&mockPlaidClient{
			syncTransactions: func(_ context.Context, _, _ string) ([]plaidclient.Transaction, []plaidclient.Transaction, []string, string, error) {
				return []plaidclient.Transaction{
					{PlaidID: settledPlaidID, PendingTransactionID: pendingPlaidID, Name: "Coffee Shop", Amount: 17.50, Date: time.Now()},
				}, nil, nil, "new-cursor", nil
			},
		},
		&mockPlaidRepo{},
		&mockBudgetProfileRepo{
			getPeriodByDate: func(_ context.Context, _ uuid.UUID, _ pgtype.Date) (db.BudgetPeriod, error) {
				return db.BudgetPeriod{ID: periodID}, nil
			},
		},
		&mockUserRepo{
			getByID: func(_ context.Context, _ uuid.UUID) (db.User, error) {
				return db.User{Plan: "pro"}, nil
			},
		},
		&mockTransactionRepo{
			getTransactionByPlaidID: func(_ context.Context, plaidID *string) (db.Transaction, error) {
				assert.Equal(t, pendingPlaidID, *plaidID, "should look up the row by the pending id carried on the settled transaction")
				return db.Transaction{ID: existingTxID, Amount: numericFromString(t, "15.00")}, nil
			},
			repointTransactionPlaidID: func(_ context.Context, arg db.RepointTransactionPlaidIDParams) (db.Transaction, error) {
				repointCalled = true
				repointArg = arg
				return db.Transaction{ID: existingTxID, Amount: numericFromString(t, "17.50")}, nil
			},
			getByID: func(_ context.Context, id uuid.UUID) (db.Transaction, error) {
				assert.Equal(t, matchedTxID, id)
				return db.Transaction{
					ID:             matchedTxID,
					BudgetPeriodID: &periodID,
					PaidDate:       pgtype.Date{Time: time.Now(), Valid: true},
				}, nil
			},
			markAsPaid: func(_ context.Context, arg db.MarkTransactionAsPaidParams) (db.Transaction, error) {
				markPaidCalled = true
				markPaidArg = arg
				return db.Transaction{ID: arg.ID}, nil
			},
			existsTransactionByPlaidID: func(_ context.Context, _ *string) (bool, error) {
				t.Fatal("a settled transaction with a matching pending id must never fall through to the ordinary dedupe/insert path")
				return false, nil
			},
			createTransactionFromPlaid: func(_ context.Context, _ db.CreateTransactionFromPlaidParams) (db.Transaction, error) {
				t.Fatal("must repoint the existing row, not insert a second, unlinked transaction")
				return db.Transaction{}, nil
			},
		},
		&mockFixedExpenseRepo{
			list: func(_ context.Context, _ uuid.UUID) ([]db.FixedExpense, error) { return nil, nil },
		},
		&mockTransactionReviewRepo{
			getByTransactionID: func(_ context.Context, transactionID uuid.UUID) (db.TransactionReview, error) {
				assert.Equal(t, existingTxID, transactionID, "should check the review on the repointed row's own (unchanged) id")
				return db.TransactionReview{Status: "confirmed", TransactionID: existingTxID, MatchedTransactionID: matchedTxID}, nil
			},
		},
		testEncKey,
	)

	encrypted, encErr := crypto.Encrypt("real-access-token", testEncKey)
	require.NoError(t, encErr)
	item := db.PlaidItem{ID: itemID, BudgetProfileID: profileID, AccessToken: encrypted}
	require.NoError(t, svc.SyncItem(context.Background(), item))

	require.True(t, repointCalled)
	assert.Equal(t, pendingPlaidID, *repointArg.OldPlaidTransactionID)
	assert.Equal(t, settledPlaidID, *repointArg.NewPlaidTransactionID)

	require.True(t, markPaidCalled, "the settled amount differs from what the bill was paid at, so it must propagate")
	assert.Equal(t, matchedTxID, markPaidArg.ID)
	assert.Equal(t, "17.50", numericToString(t, markPaidArg.Amount))
}

// A settled transaction that was never matched to anything just gets its
// local row refreshed in place — no review to consult, nothing to mark paid.
func TestSyncItem_PendingTransactionSettles_NoReview_JustRepoints(t *testing.T) {
	itemID := uuid.New()
	profileID := uuid.New()
	periodID := uuid.New()
	existingTxID := uuid.New()
	const pendingPlaidID = "plaid-tx-pending"
	const settledPlaidID = "plaid-tx-settled"

	var repointCalled, markPaidCalled bool

	svc := NewPlaidService(
		&mockPlaidClient{
			syncTransactions: func(_ context.Context, _, _ string) ([]plaidclient.Transaction, []plaidclient.Transaction, []string, string, error) {
				return []plaidclient.Transaction{
					{PlaidID: settledPlaidID, PendingTransactionID: pendingPlaidID, Name: "Grocery Run", Amount: 42.10, Date: time.Now()},
				}, nil, nil, "new-cursor", nil
			},
		},
		&mockPlaidRepo{},
		&mockBudgetProfileRepo{
			getPeriodByDate: func(_ context.Context, _ uuid.UUID, _ pgtype.Date) (db.BudgetPeriod, error) {
				return db.BudgetPeriod{ID: periodID}, nil
			},
		},
		&mockUserRepo{
			getByID: func(_ context.Context, _ uuid.UUID) (db.User, error) { return db.User{Plan: "pro"}, nil },
		},
		&mockTransactionRepo{
			getTransactionByPlaidID: func(_ context.Context, _ *string) (db.Transaction, error) {
				return db.Transaction{ID: existingTxID, Amount: numericFromString(t, "42.10")}, nil
			},
			repointTransactionPlaidID: func(_ context.Context, arg db.RepointTransactionPlaidIDParams) (db.Transaction, error) {
				repointCalled = true
				return db.Transaction{ID: existingTxID, Amount: arg.Amount}, nil
			},
			markAsPaid: func(_ context.Context, arg db.MarkTransactionAsPaidParams) (db.Transaction, error) {
				markPaidCalled = true
				return db.Transaction{}, nil
			},
			createTransactionFromPlaid: func(_ context.Context, _ db.CreateTransactionFromPlaidParams) (db.Transaction, error) {
				t.Fatal("must not insert a second transaction for a settled row that already exists locally")
				return db.Transaction{}, nil
			},
		},
		&mockFixedExpenseRepo{
			list: func(_ context.Context, _ uuid.UUID) ([]db.FixedExpense, error) { return nil, nil },
		},
		&mockTransactionReviewRepo{
			// No review configured — GetByTransactionID's default (NotFound)
			// applies, matching an unreviewed plain import.
		},
		testEncKey,
	)

	encrypted, encErr := crypto.Encrypt("real-access-token", testEncKey)
	require.NoError(t, encErr)
	item := db.PlaidItem{ID: itemID, BudgetProfileID: profileID, AccessToken: encrypted}
	require.NoError(t, svc.SyncItem(context.Background(), item))

	assert.True(t, repointCalled)
	assert.False(t, markPaidCalled, "nothing was ever marked paid off this transaction, so nothing should be re-marked")
}

// A review still awaiting user confirmation needs no special handling at
// all: its transaction_id never changes across a repoint, so it already
// points at the refreshed row. Only a *confirmed* review's paid amount is
// ever touched.
func TestSyncItem_PendingTransactionSettles_PendingReview_DoesNotMarkPaid(t *testing.T) {
	itemID := uuid.New()
	profileID := uuid.New()
	periodID := uuid.New()
	existingTxID := uuid.New()
	matchedTxID := uuid.New()
	const pendingPlaidID = "plaid-tx-pending"
	const settledPlaidID = "plaid-tx-settled"

	var markPaidCalled bool

	svc := NewPlaidService(
		&mockPlaidClient{
			syncTransactions: func(_ context.Context, _, _ string) ([]plaidclient.Transaction, []plaidclient.Transaction, []string, string, error) {
				return []plaidclient.Transaction{
					{PlaidID: settledPlaidID, PendingTransactionID: pendingPlaidID, Name: "Hardware Store", Amount: 88.00, Date: time.Now()},
				}, nil, nil, "new-cursor", nil
			},
		},
		&mockPlaidRepo{},
		&mockBudgetProfileRepo{
			getPeriodByDate: func(_ context.Context, _ uuid.UUID, _ pgtype.Date) (db.BudgetPeriod, error) {
				return db.BudgetPeriod{ID: periodID}, nil
			},
		},
		&mockUserRepo{
			getByID: func(_ context.Context, _ uuid.UUID) (db.User, error) { return db.User{Plan: "pro"}, nil },
		},
		&mockTransactionRepo{
			getTransactionByPlaidID: func(_ context.Context, _ *string) (db.Transaction, error) {
				return db.Transaction{ID: existingTxID, Amount: numericFromString(t, "80.00")}, nil
			},
			repointTransactionPlaidID: func(_ context.Context, arg db.RepointTransactionPlaidIDParams) (db.Transaction, error) {
				return db.Transaction{ID: existingTxID, Amount: arg.Amount}, nil
			},
			// A real, resolvable matched transaction with a different amount —
			// so that if the pending/confirmed status guard were ever removed,
			// this test would actually reach and catch the MarkAsPaid call
			// rather than passing by accident because there was nothing to
			// propagate to.
			getByID: func(_ context.Context, id uuid.UUID) (db.Transaction, error) {
				assert.Equal(t, matchedTxID, id)
				return db.Transaction{ID: matchedTxID, BudgetPeriodID: &periodID}, nil
			},
			markAsPaid: func(_ context.Context, _ db.MarkTransactionAsPaidParams) (db.Transaction, error) {
				markPaidCalled = true
				return db.Transaction{}, nil
			},
		},
		&mockFixedExpenseRepo{
			list: func(_ context.Context, _ uuid.UUID) ([]db.FixedExpense, error) { return nil, nil },
		},
		&mockTransactionReviewRepo{
			getByTransactionID: func(_ context.Context, _ uuid.UUID) (db.TransactionReview, error) {
				return db.TransactionReview{Status: "pending", MatchedTransactionID: matchedTxID}, nil
			},
		},
		testEncKey,
	)

	encrypted, encErr := crypto.Encrypt("real-access-token", testEncKey)
	require.NoError(t, encErr)
	item := db.PlaidItem{ID: itemID, BudgetProfileID: profileID, AccessToken: encrypted}
	require.NoError(t, svc.SyncItem(context.Background(), item))

	assert.False(t, markPaidCalled, "a not-yet-confirmed review must not have anything marked paid on its behalf")
}

// A confirmed review whose settled amount happens to match the pending
// amount exactly must not re-run MarkAsPaid at all — nothing changed, so
// there is nothing to propagate.
func TestSyncItem_PendingTransactionSettles_ConfirmedReview_SameAmount_SkipsMarkPaid(t *testing.T) {
	itemID := uuid.New()
	profileID := uuid.New()
	periodID := uuid.New()
	existingTxID := uuid.New()
	matchedTxID := uuid.New()
	const pendingPlaidID = "plaid-tx-pending"
	const settledPlaidID = "plaid-tx-settled"

	var markPaidCalled bool

	svc := NewPlaidService(
		&mockPlaidClient{
			syncTransactions: func(_ context.Context, _, _ string) ([]plaidclient.Transaction, []plaidclient.Transaction, []string, string, error) {
				return []plaidclient.Transaction{
					{PlaidID: settledPlaidID, PendingTransactionID: pendingPlaidID, Name: "Coffee Shop", Amount: 15.00, Date: time.Now()},
				}, nil, nil, "new-cursor", nil
			},
		},
		&mockPlaidRepo{},
		&mockBudgetProfileRepo{
			getPeriodByDate: func(_ context.Context, _ uuid.UUID, _ pgtype.Date) (db.BudgetPeriod, error) {
				return db.BudgetPeriod{ID: periodID}, nil
			},
		},
		&mockUserRepo{
			getByID: func(_ context.Context, _ uuid.UUID) (db.User, error) { return db.User{Plan: "pro"}, nil },
		},
		&mockTransactionRepo{
			getTransactionByPlaidID: func(_ context.Context, _ *string) (db.Transaction, error) {
				return db.Transaction{ID: existingTxID, Amount: numericFromString(t, "15.00")}, nil
			},
			repointTransactionPlaidID: func(_ context.Context, arg db.RepointTransactionPlaidIDParams) (db.Transaction, error) {
				return db.Transaction{ID: existingTxID, Amount: numericFromString(t, "15.00")}, nil
			},
			getByID: func(_ context.Context, id uuid.UUID) (db.Transaction, error) {
				return db.Transaction{ID: matchedTxID, BudgetPeriodID: &periodID}, nil
			},
			markAsPaid: func(_ context.Context, _ db.MarkTransactionAsPaidParams) (db.Transaction, error) {
				markPaidCalled = true
				return db.Transaction{}, nil
			},
		},
		&mockFixedExpenseRepo{
			list: func(_ context.Context, _ uuid.UUID) ([]db.FixedExpense, error) { return nil, nil },
		},
		&mockTransactionReviewRepo{
			getByTransactionID: func(_ context.Context, _ uuid.UUID) (db.TransactionReview, error) {
				return db.TransactionReview{Status: "confirmed", MatchedTransactionID: matchedTxID}, nil
			},
		},
		testEncKey,
	)

	encrypted, encErr := crypto.Encrypt("real-access-token", testEncKey)
	require.NoError(t, encErr)
	item := db.PlaidItem{ID: itemID, BudgetProfileID: profileID, AccessToken: encrypted}
	require.NoError(t, svc.SyncItem(context.Background(), item))

	assert.False(t, markPaidCalled, "amount is unchanged, so nothing needs to be re-marked paid")
}

// A repoint that fails at the database level must not fall back to inserting
// a fresh transaction — that would recreate the exact duplicate this whole
// mechanism exists to prevent. It is logged and dropped, the same posture
// every other per-transaction error already takes in this file (the sync
// cursor still advances; a permanently-failing repoint is not retried, no
// different from a permanently-failing insert today).
func TestSyncItem_PendingTransactionSettles_RepointFails_DoesNotFallBackToInsert(t *testing.T) {
	itemID := uuid.New()
	profileID := uuid.New()
	periodID := uuid.New()
	existingTxID := uuid.New()
	const pendingPlaidID = "plaid-tx-pending"
	const settledPlaidID = "plaid-tx-settled"

	svc := NewPlaidService(
		&mockPlaidClient{
			syncTransactions: func(_ context.Context, _, _ string) ([]plaidclient.Transaction, []plaidclient.Transaction, []string, string, error) {
				return []plaidclient.Transaction{
					{PlaidID: settledPlaidID, PendingTransactionID: pendingPlaidID, Name: "Coffee Shop", Amount: 17.50, Date: time.Now()},
				}, nil, nil, "new-cursor", nil
			},
		},
		&mockPlaidRepo{},
		&mockBudgetProfileRepo{
			getPeriodByDate: func(_ context.Context, _ uuid.UUID, _ pgtype.Date) (db.BudgetPeriod, error) {
				return db.BudgetPeriod{ID: periodID}, nil
			},
		},
		&mockUserRepo{
			getByID: func(_ context.Context, _ uuid.UUID) (db.User, error) { return db.User{Plan: "pro"}, nil },
		},
		&mockTransactionRepo{
			getTransactionByPlaidID: func(_ context.Context, _ *string) (db.Transaction, error) {
				return db.Transaction{ID: existingTxID, Amount: numericFromString(t, "15.00")}, nil
			},
			repointTransactionPlaidID: func(_ context.Context, _ db.RepointTransactionPlaidIDParams) (db.Transaction, error) {
				return db.Transaction{}, errors.New("connection reset")
			},
			createTransactionFromPlaid: func(_ context.Context, _ db.CreateTransactionFromPlaidParams) (db.Transaction, error) {
				t.Fatal("a failed repoint must not fall back to inserting a duplicate")
				return db.Transaction{}, nil
			},
		},
		&mockFixedExpenseRepo{
			list: func(_ context.Context, _ uuid.UUID) ([]db.FixedExpense, error) { return nil, nil },
		},
		&mockTransactionReviewRepo{},
		testEncKey,
	)

	encrypted, encErr := crypto.Encrypt("real-access-token", testEncKey)
	require.NoError(t, encErr)
	item := db.PlaidItem{ID: itemID, BudgetProfileID: profileID, AccessToken: encrypted}
	require.NoError(t, svc.SyncItem(context.Background(), item), "a per-transaction repoint failure must not fail the whole sync run")
}

// If the matched (fixed-expense) transaction a confirmed review points at
// can no longer be read, the settle still succeeds — the repointed row and
// its review are intact either way — it just can't propagate the new amount.
func TestSyncItem_PendingTransactionSettles_MatchedTransactionUnreadable_SkipsMarkPaid(t *testing.T) {
	itemID := uuid.New()
	profileID := uuid.New()
	periodID := uuid.New()
	existingTxID := uuid.New()
	matchedTxID := uuid.New()
	const pendingPlaidID = "plaid-tx-pending"
	const settledPlaidID = "plaid-tx-settled"

	var markPaidCalled bool

	svc := NewPlaidService(
		&mockPlaidClient{
			syncTransactions: func(_ context.Context, _, _ string) ([]plaidclient.Transaction, []plaidclient.Transaction, []string, string, error) {
				return []plaidclient.Transaction{
					{PlaidID: settledPlaidID, PendingTransactionID: pendingPlaidID, Name: "Coffee Shop", Amount: 17.50, Date: time.Now()},
				}, nil, nil, "new-cursor", nil
			},
		},
		&mockPlaidRepo{},
		&mockBudgetProfileRepo{
			getPeriodByDate: func(_ context.Context, _ uuid.UUID, _ pgtype.Date) (db.BudgetPeriod, error) {
				return db.BudgetPeriod{ID: periodID}, nil
			},
		},
		&mockUserRepo{
			getByID: func(_ context.Context, _ uuid.UUID) (db.User, error) { return db.User{Plan: "pro"}, nil },
		},
		&mockTransactionRepo{
			getTransactionByPlaidID: func(_ context.Context, _ *string) (db.Transaction, error) {
				return db.Transaction{ID: existingTxID, Amount: numericFromString(t, "15.00")}, nil
			},
			repointTransactionPlaidID: func(_ context.Context, arg db.RepointTransactionPlaidIDParams) (db.Transaction, error) {
				return db.Transaction{ID: existingTxID, Amount: arg.Amount}, nil
			},
			// GetByID's default (no field configured) is apperr.NotFound — the
			// matched transaction has, somehow, already been deleted.
			markAsPaid: func(_ context.Context, _ db.MarkTransactionAsPaidParams) (db.Transaction, error) {
				markPaidCalled = true
				return db.Transaction{}, nil
			},
		},
		&mockFixedExpenseRepo{
			list: func(_ context.Context, _ uuid.UUID) ([]db.FixedExpense, error) { return nil, nil },
		},
		&mockTransactionReviewRepo{
			getByTransactionID: func(_ context.Context, _ uuid.UUID) (db.TransactionReview, error) {
				return db.TransactionReview{Status: "confirmed", MatchedTransactionID: matchedTxID}, nil
			},
		},
		testEncKey,
	)

	encrypted, encErr := crypto.Encrypt("real-access-token", testEncKey)
	require.NoError(t, encErr)
	item := db.PlaidItem{ID: itemID, BudgetProfileID: profileID, AccessToken: encrypted}
	require.NoError(t, svc.SyncItem(context.Background(), item))

	assert.False(t, markPaidCalled, "nothing to mark paid without a readable matched transaction")
}

// A MarkAsPaid failure while propagating the settled amount must not be
// mistaken for the repoint itself failing — the row and its review are
// already safely updated by this point; only the amount propagation failed.
func TestSyncItem_PendingTransactionSettles_MarkPaidFails_RepointStillSucceeds(t *testing.T) {
	itemID := uuid.New()
	profileID := uuid.New()
	periodID := uuid.New()
	existingTxID := uuid.New()
	matchedTxID := uuid.New()
	const pendingPlaidID = "plaid-tx-pending"
	const settledPlaidID = "plaid-tx-settled"

	svc := NewPlaidService(
		&mockPlaidClient{
			syncTransactions: func(_ context.Context, _, _ string) ([]plaidclient.Transaction, []plaidclient.Transaction, []string, string, error) {
				return []plaidclient.Transaction{
					{PlaidID: settledPlaidID, PendingTransactionID: pendingPlaidID, Name: "Coffee Shop", Amount: 17.50, Date: time.Now()},
				}, nil, nil, "new-cursor", nil
			},
		},
		&mockPlaidRepo{},
		&mockBudgetProfileRepo{
			getPeriodByDate: func(_ context.Context, _ uuid.UUID, _ pgtype.Date) (db.BudgetPeriod, error) {
				return db.BudgetPeriod{ID: periodID}, nil
			},
		},
		&mockUserRepo{
			getByID: func(_ context.Context, _ uuid.UUID) (db.User, error) { return db.User{Plan: "pro"}, nil },
		},
		&mockTransactionRepo{
			getTransactionByPlaidID: func(_ context.Context, _ *string) (db.Transaction, error) {
				return db.Transaction{ID: existingTxID, Amount: numericFromString(t, "15.00")}, nil
			},
			repointTransactionPlaidID: func(_ context.Context, arg db.RepointTransactionPlaidIDParams) (db.Transaction, error) {
				return db.Transaction{ID: existingTxID, Amount: arg.Amount}, nil
			},
			getByID: func(_ context.Context, id uuid.UUID) (db.Transaction, error) {
				return db.Transaction{ID: matchedTxID, BudgetPeriodID: &periodID}, nil
			},
			markAsPaid: func(_ context.Context, _ db.MarkTransactionAsPaidParams) (db.Transaction, error) {
				return db.Transaction{}, errors.New("connection reset")
			},
		},
		&mockFixedExpenseRepo{
			list: func(_ context.Context, _ uuid.UUID) ([]db.FixedExpense, error) { return nil, nil },
		},
		&mockTransactionReviewRepo{
			getByTransactionID: func(_ context.Context, _ uuid.UUID) (db.TransactionReview, error) {
				return db.TransactionReview{Status: "confirmed", MatchedTransactionID: matchedTxID}, nil
			},
		},
		testEncKey,
	)

	encrypted, encErr := crypto.Encrypt("real-access-token", testEncKey)
	require.NoError(t, encErr)
	item := db.PlaidItem{ID: itemID, BudgetProfileID: profileID, AccessToken: encrypted}
	require.NoError(t, svc.SyncItem(context.Background(), item), "a downstream amount-propagation failure must not fail the whole sync run")
}

// TestSyncItem_NotifiesOnceForPlainImports_NotOncePerTransaction covers the
// most common sync outcome — several imported transactions that don't match
// any fixed expense at all — and asserts exactly one new_transaction
// notification fires for the whole run, not one per row.
func TestSyncItem_NotifiesOnceForPlainImports_NotOncePerTransaction(t *testing.T) {
	itemID := uuid.New()
	profileID := uuid.New()
	periodID := uuid.New()
	encrypted, err := crypto.Encrypt("real-access-token", testEncKey)
	require.NoError(t, err)

	var createCount int
	var lastBody string
	notifRepo := &mockNotifRepo{
		getBudgetSubscribers: func(_ context.Context, _ uuid.UUID, alertType string) ([]db.AlertSubscription, error) {
			if alertType == "new_transaction" {
				return []db.AlertSubscription{{ID: uuid.New(), UserID: uuid.New(), AlertType: "new_transaction", Channel: "in_app"}}, nil
			}
			return nil, nil
		},
		create: func(_ context.Context, arg db.CreateNotificationParams) (db.Notification, error) {
			createCount++
			lastBody = arg.Body
			return db.Notification{ID: uuid.New()}, nil
		},
	}
	notifSvc := newTestNotifSvc(notifRepo, &mockBudgetProfileRepo{})

	svc := NewPlaidService(
		&mockPlaidClient{
			syncTransactions: func(_ context.Context, _, _ string) ([]plaidclient.Transaction, []plaidclient.Transaction, []string, string, error) {
				return []plaidclient.Transaction{
					{PlaidID: "plaid-tx-1", Name: "Whole Foods", Amount: 42.10, Date: time.Now()},
					{PlaidID: "plaid-tx-2", Name: "Shell Gas", Amount: 30.00, Date: time.Now()},
					{PlaidID: "plaid-tx-3", Name: "Amazon", Amount: 19.99, Date: time.Now()},
				}, nil, nil, "new-cursor", nil
			},
		},
		&mockPlaidRepo{},
		&mockBudgetProfileRepo{
			getPeriodByDate: func(_ context.Context, _ uuid.UUID, _ pgtype.Date) (db.BudgetPeriod, error) {
				return db.BudgetPeriod{ID: periodID}, nil
			},
		},
		&mockUserRepo{
			getByID: func(_ context.Context, _ uuid.UUID) (db.User, error) {
				return db.User{Plan: "pro"}, nil
			},
		},
		&mockTransactionRepo{
			createTransactionFromPlaid: func(_ context.Context, _ db.CreateTransactionFromPlaidParams) (db.Transaction, error) {
				return db.Transaction{ID: uuid.New()}, nil
			},
		},
		&mockFixedExpenseRepo{
			list: func(_ context.Context, _ uuid.UUID) ([]db.FixedExpense, error) {
				return nil, nil // no fixed expenses to match against — every import is "plain"
			},
		},
		&mockTransactionReviewRepo{},
		testEncKey,
	).WithNotifications(notifSvc)

	item := db.PlaidItem{ID: itemID, BudgetProfileID: profileID, AccessToken: encrypted}
	require.NoError(t, svc.SyncItem(context.Background(), item))

	require.Equal(t, 1, createCount, "3 plain imports should still produce exactly one aggregated notification")
	assert.Contains(t, lastBody, "3", "the aggregated notification should mention the actual count")
}

// TestSyncItem_NotifiesOnceForQueuedReviews_NotOncePerTransaction mirrors the
// plain-import case above but for transactions that score high enough
// against a fixed expense to be queued for review (not auto-confirmed).
func TestSyncItem_NotifiesOnceForQueuedReviews_NotOncePerTransaction(t *testing.T) {
	itemID := uuid.New()
	profileID := uuid.New()
	periodID := uuid.New()
	feID := uuid.New()
	unpaidTxID := uuid.New()
	pmID := uuid.New()
	encrypted, err := crypto.Encrypt("real-access-token", testEncKey)
	require.NoError(t, err)

	var createCount int
	var lastBody string
	notifRepo := &mockNotifRepo{
		getBudgetSubscribers: func(_ context.Context, _ uuid.UUID, alertType string) ([]db.AlertSubscription, error) {
			if alertType == "review_pending" {
				return []db.AlertSubscription{{ID: uuid.New(), UserID: uuid.New(), AlertType: "review_pending", Channel: "in_app"}}, nil
			}
			return nil, nil
		},
		create: func(_ context.Context, arg db.CreateNotificationParams) (db.Notification, error) {
			createCount++
			lastBody = arg.Body
			return db.Notification{ID: uuid.New()}, nil
		},
	}
	notifSvc := newTestNotifSvc(notifRepo, &mockBudgetProfileRepo{})

	svc := NewPlaidService(
		&mockPlaidClient{
			syncTransactions: func(_ context.Context, _, _ string) ([]plaidclient.Transaction, []plaidclient.Transaction, []string, string, error) {
				// Amount match (40) + word-overlap name match, no alias (20) +
				// payment method match (20) = 80 — reaches the review queue
				// threshold without an alias hit, so it lands in the "queued"
				// branch rather than auto-confirm.
				return []plaidclient.Transaction{
					{PlaidID: "plaid-tx-1", Name: "Creator Support Membership", Amount: 15.00, Date: time.Now(), AccountID: "acct-1"},
					{PlaidID: "plaid-tx-2", Name: "Creator Support Membership", Amount: 15.00, Date: time.Now(), AccountID: "acct-1"},
				}, nil, nil, "new-cursor", nil
			},
		},
		&mockPlaidRepo{},
		&mockBudgetProfileRepo{
			getPeriodByDate: func(_ context.Context, _ uuid.UUID, _ pgtype.Date) (db.BudgetPeriod, error) {
				return db.BudgetPeriod{ID: periodID}, nil
			},
		},
		&mockUserRepo{
			getByID: func(_ context.Context, _ uuid.UUID) (db.User, error) {
				return db.User{Plan: "pro"}, nil
			},
		},
		&mockTransactionRepo{
			createTransactionFromPlaid: func(_ context.Context, _ db.CreateTransactionFromPlaidParams) (db.Transaction, error) {
				return db.Transaction{ID: uuid.New()}, nil
			},
			getPaymentMethodByPlaidAccountID: func(_ context.Context, _ string) (db.PaymentMethod, error) {
				return db.PaymentMethod{ID: pmID}, nil
			},
		},
		&mockFixedExpenseRepo{
			list: func(_ context.Context, _ uuid.UUID) ([]db.FixedExpense, error) {
				fe := makeFixedExpense(t, feID, "Creator Support", "15.00")
				fe.PaymentMethodID = &pmID
				return []db.FixedExpense{fe}, nil
			},
			getUnpaidTransactionInPer: func(_ context.Context, arg db.GetUnpaidTransactionByFixedExpenseInPeriodParams) (db.Transaction, error) {
				// Scoped to the imported transaction's own period (issue #41).
				if arg.BudgetPeriodID != periodID {
					return db.Transaction{}, apperr.NotFound("transaction", arg.FixedExpenseID.String())
				}
				return db.Transaction{ID: unpaidTxID, BudgetPeriodID: &periodID, PlannedAmount: numericFromString(t, "15.00")}, nil
			},
		},
		&mockTransactionReviewRepo{
			create: func(_ context.Context, _, transactionID, matchedTransactionID uuid.UUID, _ float64) (db.TransactionReview, error) {
				return db.TransactionReview{ID: uuid.New(), TransactionID: transactionID, MatchedTransactionID: matchedTransactionID}, nil
			},
		},
		testEncKey,
	).WithNotifications(notifSvc)

	item := db.PlaidItem{ID: itemID, BudgetProfileID: profileID, AccessToken: encrypted}
	require.NoError(t, svc.SyncItem(context.Background(), item))

	require.Equal(t, 1, createCount, "2 queued-for-review imports should still produce exactly one aggregated notification")
	assert.Contains(t, lastBody, "2", "the aggregated notification should mention the actual count")
}

// ── SyncAll: grouping, entitlement reporting, per-account attribution ─────────

// syncAllSvc builds a service whose Plaid client returns `added` for every
// item, and whose users all sit on the given plan.
func syncAllSvc(t *testing.T, plan string, items []db.PlaidItem, added []plaidclient.Transaction) *PlaidService {
	t.Helper()
	return NewPlaidService(
		&mockPlaidClient{
			syncTransactions: func(_ context.Context, _, _ string) ([]plaidclient.Transaction, []plaidclient.Transaction, []string, string, error) {
				return added, nil, nil, "next-cursor", nil
			},
		},
		&mockPlaidRepo{
			listForSync: func(_ context.Context) ([]db.PlaidItem, error) { return items, nil },
		},
		&mockBudgetProfileRepo{
			getPeriodByDate: func(_ context.Context, profileID uuid.UUID, _ pgtype.Date) (db.BudgetPeriod, error) {
				return db.BudgetPeriod{ID: uuid.New(), BudgetProfileID: profileID}, nil
			},
		},
		&mockUserRepo{
			getByID: func(_ context.Context, _ uuid.UUID) (db.User, error) {
				return db.User{Plan: plan}, nil
			},
		},
		&mockTransactionRepo{},
		&mockFixedExpenseRepo{},
		&mockTransactionReviewRepo{},
		testEncKey,
	)
}

func encryptedToken(t *testing.T) string {
	t.Helper()
	enc, err := crypto.Encrypt("real-access-token", testEncKey)
	require.NoError(t, err)
	return enc
}

func TestSyncAll_GroupsConnectionsByBudgetProfile(t *testing.T) {
	profileA, profileB := uuid.New(), uuid.New()
	token := encryptedToken(t)
	items := []db.PlaidItem{
		{ID: uuid.New(), BudgetProfileID: profileA, AccessToken: token},
		{ID: uuid.New(), BudgetProfileID: profileB, AccessToken: token},
		{ID: uuid.New(), BudgetProfileID: profileA, AccessToken: token},
	}

	profiles, err := syncAllSvc(t, "pro", items, nil).SyncAll(context.Background())

	require.NoError(t, err)
	require.Len(t, profiles, 2, "one entry per budget, not per connection")
	byProfile := map[uuid.UUID]int{}
	for _, p := range profiles {
		byProfile[p.ProfileID] = len(p.Items)
	}
	assert.Equal(t, 2, byProfile[profileA])
	assert.Equal(t, 1, byProfile[profileB])
}

func TestSyncAll_ReportsUnentitledSkipInsteadOfSwallowingIt(t *testing.T) {
	profileID := uuid.New()
	inst := "Chase"
	items := []db.PlaidItem{
		{ID: uuid.New(), BudgetProfileID: profileID, InstitutionName: &inst, AccessToken: encryptedToken(t)},
	}

	profiles, err := syncAllSvc(t, "free", items, nil).SyncAll(context.Background())

	require.NoError(t, err)
	require.Len(t, profiles, 1)
	require.Len(t, profiles[0].Items, 1)
	result := profiles[0].Items[0]
	// Previously this returned a bare nil and logged one line, which is how a
	// connection sat unsynced for over two weeks without anyone noticing.
	assert.True(t, result.SkippedUnentitled, "a free-tier owner's connection must be reported, not silently skipped")
	assert.NoError(t, result.Err, "not being entitled is not a failure")
	assert.Equal(t, "Chase", result.InstitutionName, "the report must name the institution, not just the item UUID")
}

func TestSyncAll_AttributesImportsToTheAccountTheyCameFrom(t *testing.T) {
	profileID := uuid.New()
	token := encryptedToken(t)
	checkingID, cardID := uuid.New(), uuid.New()

	added := []plaidclient.Transaction{
		{PlaidID: "p1", Name: "Coffee", Amount: 4.5, Date: time.Now(), AccountID: "acct-checking"},
		{PlaidID: "p2", Name: "Books", Amount: 20, Date: time.Now(), AccountID: "acct-checking"},
		{PlaidID: "p3", Name: "Fuel", Amount: 40, Date: time.Now(), AccountID: "acct-card"},
	}

	svc := syncAllSvc(t, "pro", []db.PlaidItem{{ID: uuid.New(), BudgetProfileID: profileID, AccessToken: token}}, added)
	svc.transactions = &mockTransactionRepo{
		getPaymentMethodByPlaidAccountID: func(_ context.Context, plaidAccountID string) (db.PaymentMethod, error) {
			switch plaidAccountID {
			case "acct-checking":
				return db.PaymentMethod{ID: checkingID, Name: "Chase Checking ···1234"}, nil
			case "acct-card":
				return db.PaymentMethod{ID: cardID, Name: "Amex ···9012"}, nil
			}
			return db.PaymentMethod{}, errors.New("not found")
		},
	}

	profiles, err := svc.SyncAll(context.Background())

	require.NoError(t, err)
	require.Len(t, profiles, 1)
	assert.Equal(t, []AccountImport{
		{Account: "Chase Checking ···1234", Count: 2},
		{Account: "Amex ···9012", Count: 1},
	}, profiles[0].Items[0].ByAccount, "busiest account first, so the summary leads with where the activity was")
}

func TestSyncProfile_QueriesOnlyThatProfilesConnections(t *testing.T) {
	profileID := uuid.New()
	var gotProfileID uuid.UUID
	items := []db.PlaidItem{{ID: uuid.New(), BudgetProfileID: profileID, AccessToken: encryptedToken(t)}}

	svc := syncAllSvc(t, "pro", nil, nil)
	svc.items = &mockPlaidRepo{
		listForProfileSync: func(_ context.Context, id uuid.UUID) ([]db.PlaidItem, error) {
			gotProfileID = id
			return items, nil
		},
	}

	result, err := svc.SyncProfile(context.Background(), profileID)

	require.NoError(t, err)
	assert.Equal(t, profileID, gotProfileID, "must query by the profile passed in, not sync every connection")
	assert.Equal(t, profileID, result.ProfileID)
	require.Len(t, result.Items, 1)
}

func TestSyncProfile_ItemErrorDoesNotBlockOthers(t *testing.T) {
	profileID := uuid.New()
	okToken := encryptedToken(t)
	badToken, err := crypto.Encrypt("bad-token", testEncKey)
	require.NoError(t, err)
	items := []db.PlaidItem{
		{ID: uuid.New(), BudgetProfileID: profileID, AccessToken: badToken},
		{ID: uuid.New(), BudgetProfileID: profileID, AccessToken: okToken},
	}

	svc := syncAllSvc(t, "pro", items, nil)
	svc.items = &mockPlaidRepo{
		listForProfileSync: func(_ context.Context, _ uuid.UUID) ([]db.PlaidItem, error) { return items, nil },
	}
	svc.plaid = &mockPlaidClient{
		syncTransactions: func(_ context.Context, accessToken, _ string) ([]plaidclient.Transaction, []plaidclient.Transaction, []string, string, error) {
			if accessToken == "bad-token" {
				return nil, nil, nil, "", errors.New("plaid: rate limited")
			}
			return nil, nil, nil, "next-cursor", nil
		},
	}

	result, err := svc.SyncProfile(context.Background(), profileID)

	require.NoError(t, err, "one connection failing must not fail the whole profile sync")
	require.Len(t, result.Items, 2)
	assert.Error(t, result.Items[0].Err)
	assert.NoError(t, result.Items[1].Err, "the second connection must still be attempted and succeed")
}

func TestSyncProfile_NoConnections_ReturnsEmptyResult(t *testing.T) {
	profileID := uuid.New()
	svc := syncAllSvc(t, "pro", nil, nil)
	svc.items = &mockPlaidRepo{
		listForProfileSync: func(_ context.Context, _ uuid.UUID) ([]db.PlaidItem, error) { return nil, nil },
	}

	result, err := svc.SyncProfile(context.Background(), profileID)

	require.NoError(t, err)
	assert.Empty(t, result.Items)
}

func TestSyncProfile_RepoErrorPropagates(t *testing.T) {
	profileID := uuid.New()
	svc := syncAllSvc(t, "pro", nil, nil)
	svc.items = &mockPlaidRepo{
		listForProfileSync: func(_ context.Context, _ uuid.UUID) ([]db.PlaidItem, error) {
			return nil, errors.New("db unavailable")
		},
	}

	_, err := svc.SyncProfile(context.Background(), profileID)

	assert.Error(t, err)
}

func TestSortedAccountImports_IsDeterministicOnTies(t *testing.T) {
	// Map iteration order is random; a run's notification body and its tests
	// must not be.
	for i := 0; i < 20; i++ {
		got := sortedAccountImports(map[string]int{"Zebra": 2, "Apple": 2, "Middle": 5})
		assert.Equal(t, []AccountImport{
			{Account: "Middle", Count: 5},
			{Account: "Apple", Count: 2},
			{Account: "Zebra", Count: 2},
		}, got)
	}
}

func TestListSyncWarnings_FlagsTheCallersOwnConnections(t *testing.T) {
	callerID, otherID := uuid.New(), uuid.New()
	profileID := uuid.New()

	svc := NewPlaidService(
		&mockPlaidClient{},
		&mockPlaidRepo{
			listUnsyncable: func(_ context.Context, _ uuid.UUID) ([]db.ListUnsyncableConnectionsForUserRow, error) {
				return []db.ListUnsyncableConnectionsForUserRow{
					{BudgetProfileID: profileID, BudgetName: "Household", MemberUserID: otherID, MemberName: "Alex", ConnectionCount: 2},
					{BudgetProfileID: profileID, BudgetName: "Household", MemberUserID: callerID, MemberName: "Me", ConnectionCount: 1},
				}, nil
			},
		},
		&mockBudgetProfileRepo{}, &mockUserRepo{}, &mockTransactionRepo{},
		&mockFixedExpenseRepo{}, &mockTransactionReviewRepo{}, testEncKey,
	)

	warnings, err := svc.ListSyncWarnings(context.Background(), callerID)

	require.NoError(t, err)
	require.Len(t, warnings, 2)
	assert.False(t, warnings[0].IsCurrentUser, "someone else's connections are named")
	assert.Equal(t, int32(2), warnings[0].ConnectionCount)
	assert.True(t, warnings[1].IsCurrentUser, "the caller's own get an upgrade prompt instead of a name")
}

// A transaction that posts after its period closes must not settle anything.
// Before issue #41, the import was filed into the archived period it was dated
// in, while the match target was drawn only from *live* periods newest-first —
// so a late payment reached forward and marked the following period's bill
// paid, and the review recording it landed in the archived period where
// ListTransactionReviews filtered it out. Both clients then showed the bill as
// paid with no link, and that period's real payment had nothing left to match.
func TestSyncItem_ArchivedPeriodImport_DoesNotMarkAnythingPaid(t *testing.T) {
	itemID := uuid.New()
	profileID := uuid.New()
	archivedPeriodID := uuid.New()
	feID := uuid.New()
	encrypted, err := crypto.Encrypt("real-access-token", testEncKey)
	require.NoError(t, err)

	var markedPaidCalled, excludedCalled, reviewCreated bool
	var unpaidLookupCalled bool

	svc := NewPlaidService(
		&mockPlaidClient{
			syncTransactions: func(_ context.Context, _, _ string) ([]plaidclient.Transaction, []plaidclient.Transaction, []string, string, error) {
				return []plaidclient.Transaction{
					{PlaidID: "plaid-tx-late", Name: "Patreon", Amount: 15.00, Date: time.Now().AddDate(0, 0, -20)},
				}, nil, nil, "new-cursor", nil
			},
		},
		&mockPlaidRepo{},
		&mockBudgetProfileRepo{
			getPeriodByDate: func(_ context.Context, _ uuid.UUID, _ pgtype.Date) (db.BudgetPeriod, error) {
				return db.BudgetPeriod{ID: archivedPeriodID, IsArchived: true}, nil
			},
		},
		&mockUserRepo{
			getByID: func(_ context.Context, _ uuid.UUID) (db.User, error) {
				return db.User{Plan: "pro"}, nil
			},
		},
		&mockTransactionRepo{
			createTransactionFromPlaid: func(_ context.Context, _ db.CreateTransactionFromPlaidParams) (db.Transaction, error) {
				return db.Transaction{ID: uuid.New()}, nil
			},
			markAsPaid: func(_ context.Context, arg db.MarkTransactionAsPaidParams) (db.Transaction, error) {
				markedPaidCalled = true
				return db.Transaction{ID: arg.ID}, nil
			},
			setExcluded: func(_ context.Context, arg db.SetTransactionExcludedParams) (db.Transaction, error) {
				excludedCalled = true
				return db.Transaction{ID: arg.ID}, nil
			},
		},
		&mockFixedExpenseRepo{
			list: func(_ context.Context, _ uuid.UUID) ([]db.FixedExpense, error) {
				return []db.FixedExpense{makeFixedExpense(t, feID, "Creator Support", "15.00")}, nil
			},
			getUnpaidTransactionInPer: func(_ context.Context, _ db.GetUnpaidTransactionByFixedExpenseInPeriodParams) (db.Transaction, error) {
				unpaidLookupCalled = true
				return db.Transaction{}, apperr.NotFound("transaction", feID.String())
			},
		},
		&mockTransactionReviewRepo{
			listAliases: func(_ context.Context, _ uuid.UUID) ([]string, error) {
				return []string{"Patreon"}, nil
			},
			create: func(_ context.Context, _, transactionID, matchedTransactionID uuid.UUID, _ float64) (db.TransactionReview, error) {
				reviewCreated = true
				return db.TransactionReview{ID: uuid.New(), TransactionID: transactionID, MatchedTransactionID: matchedTransactionID}, nil
			},
		},
		testEncKey,
	)

	item := db.PlaidItem{ID: itemID, BudgetProfileID: profileID, AccessToken: encrypted}
	require.NoError(t, svc.SyncItem(context.Background(), item))

	assert.False(t, unpaidLookupCalled, "must not even look for a target when the period is archived")
	assert.False(t, markedPaidCalled, "must not mark a fixed expense paid from an archived period")
	assert.False(t, excludedCalled, "must not exclude an import in an archived period")
	assert.False(t, reviewCreated, "must not record a review against an archived period")
}

// The target must live in the imported transaction's own period. A perfect
// alias+amount match against a bill in a *different* period is not a match.
func TestSyncItem_NoTargetInOwnPeriod_DoesNotReachIntoAnother(t *testing.T) {
	itemID := uuid.New()
	profileID := uuid.New()
	periodID := uuid.New()
	otherPeriodID := uuid.New()
	feID := uuid.New()
	encrypted, err := crypto.Encrypt("real-access-token", testEncKey)
	require.NoError(t, err)

	var markedPaidCalled, reviewCreated bool
	var askedForPeriod uuid.UUID

	svc := NewPlaidService(
		&mockPlaidClient{
			syncTransactions: func(_ context.Context, _, _ string) ([]plaidclient.Transaction, []plaidclient.Transaction, []string, string, error) {
				return []plaidclient.Transaction{
					{PlaidID: "plaid-tx-1", Name: "Patreon", Amount: 15.00, Date: time.Now()},
				}, nil, nil, "new-cursor", nil
			},
		},
		&mockPlaidRepo{},
		&mockBudgetProfileRepo{
			getPeriodByDate: func(_ context.Context, _ uuid.UUID, _ pgtype.Date) (db.BudgetPeriod, error) {
				return db.BudgetPeriod{ID: periodID}, nil
			},
		},
		&mockUserRepo{
			getByID: func(_ context.Context, _ uuid.UUID) (db.User, error) {
				return db.User{Plan: "pro"}, nil
			},
		},
		&mockTransactionRepo{
			createTransactionFromPlaid: func(_ context.Context, _ db.CreateTransactionFromPlaidParams) (db.Transaction, error) {
				return db.Transaction{ID: uuid.New()}, nil
			},
			markAsPaid: func(_ context.Context, arg db.MarkTransactionAsPaidParams) (db.Transaction, error) {
				markedPaidCalled = true
				return db.Transaction{ID: arg.ID}, nil
			},
		},
		&mockFixedExpenseRepo{
			list: func(_ context.Context, _ uuid.UUID) ([]db.FixedExpense, error) {
				return []db.FixedExpense{makeFixedExpense(t, feID, "Creator Support", "15.00")}, nil
			},
			// The only unpaid bill sits in another period, so this returns nothing.
			getUnpaidTransactionInPer: func(_ context.Context, arg db.GetUnpaidTransactionByFixedExpenseInPeriodParams) (db.Transaction, error) {
				askedForPeriod = arg.BudgetPeriodID
				return db.Transaction{}, apperr.NotFound("transaction", arg.FixedExpenseID.String())
			},
		},
		&mockTransactionReviewRepo{
			listAliases: func(_ context.Context, _ uuid.UUID) ([]string, error) {
				return []string{"Patreon"}, nil
			},
			create: func(_ context.Context, _, transactionID, matchedTransactionID uuid.UUID, _ float64) (db.TransactionReview, error) {
				reviewCreated = true
				return db.TransactionReview{ID: uuid.New(), TransactionID: transactionID, MatchedTransactionID: matchedTransactionID}, nil
			},
		},
		testEncKey,
	)

	item := db.PlaidItem{ID: itemID, BudgetProfileID: profileID, AccessToken: encrypted}
	require.NoError(t, svc.SyncItem(context.Background(), item))

	assert.Equal(t, periodID, askedForPeriod, "must look for the target in the import's own period")
	assert.NotEqual(t, otherPeriodID, askedForPeriod)
	assert.False(t, markedPaidCalled, "no target in this period means nothing is marked paid")
	assert.False(t, reviewCreated, "and no review is recorded")
}

// A half-applied auto-match must not be reported as a success. The bill is
// marked paid, then the review insert fails — the run summary previously
// counted this as auto-confirmed and logged success, which is why #41 could
// only be diagnosed from the database rather than the logs.
func TestSyncItem_AutoConfirmReviewFails_NotCountedAsConfirmed(t *testing.T) {
	itemID := uuid.New()
	profileID := uuid.New()
	periodID := uuid.New()
	feID := uuid.New()
	unpaidTxID := uuid.New()
	encrypted, err := crypto.Encrypt("real-access-token", testEncKey)
	require.NoError(t, err)

	var excludedCalled bool
	var notificationBody string

	notifRepo := &mockNotifRepo{
		create: func(_ context.Context, arg db.CreateNotificationParams) (db.Notification, error) {
			notificationBody = arg.Body
			return db.Notification{ID: uuid.New()}, nil
		},
	}

	svc := NewPlaidService(
		&mockPlaidClient{
			syncTransactions: func(_ context.Context, _, _ string) ([]plaidclient.Transaction, []plaidclient.Transaction, []string, string, error) {
				return []plaidclient.Transaction{
					{PlaidID: "plaid-tx-1", Name: "Patreon", Amount: 15.00, Date: time.Now()},
				}, nil, nil, "new-cursor", nil
			},
		},
		&mockPlaidRepo{},
		&mockBudgetProfileRepo{
			getPeriodByDate: func(_ context.Context, _ uuid.UUID, _ pgtype.Date) (db.BudgetPeriod, error) {
				return db.BudgetPeriod{ID: periodID}, nil
			},
		},
		&mockUserRepo{
			getByID: func(_ context.Context, _ uuid.UUID) (db.User, error) {
				return db.User{Plan: "pro"}, nil
			},
		},
		&mockTransactionRepo{
			createTransactionFromPlaid: func(_ context.Context, _ db.CreateTransactionFromPlaidParams) (db.Transaction, error) {
				return db.Transaction{ID: uuid.New()}, nil
			},
			markAsPaid: func(_ context.Context, arg db.MarkTransactionAsPaidParams) (db.Transaction, error) {
				return db.Transaction{ID: arg.ID}, nil
			},
			setExcluded: func(_ context.Context, arg db.SetTransactionExcludedParams) (db.Transaction, error) {
				excludedCalled = true
				return db.Transaction{ID: arg.ID}, nil
			},
		},
		&mockFixedExpenseRepo{
			list: func(_ context.Context, _ uuid.UUID) ([]db.FixedExpense, error) {
				return []db.FixedExpense{makeFixedExpense(t, feID, "Creator Support", "15.00")}, nil
			},
			getUnpaidTransactionInPer: func(_ context.Context, _ db.GetUnpaidTransactionByFixedExpenseInPeriodParams) (db.Transaction, error) {
				return db.Transaction{ID: unpaidTxID, BudgetPeriodID: &periodID, PlannedAmount: numericFromString(t, "15.00")}, nil
			},
		},
		&mockTransactionReviewRepo{
			listAliases: func(_ context.Context, _ uuid.UUID) ([]string, error) {
				return []string{"Patreon"}, nil
			},
			create: func(_ context.Context, _, _, _ uuid.UUID, _ float64) (db.TransactionReview, error) {
				return db.TransactionReview{}, errors.New("conflict")
			},
		},
		testEncKey,
	).WithNotifications(newTestNotifSvc(notifRepo, &mockBudgetProfileRepo{}))

	item := db.PlaidItem{ID: itemID, BudgetProfileID: profileID, AccessToken: encrypted}
	require.NoError(t, svc.SyncItem(context.Background(), item))

	assert.False(t, excludedCalled, "must stop once the review can't be recorded, rather than excluding an import with no link")
	assert.NotContains(t, notificationBody, "auto-confirmed", "a failed match must not be reported to the user as auto-confirmed")
}

// Plaid leaves every payment_meta field null for anything that is not an
// inter-bank transfer, which is most transactions. Storing "" would make an
// absent value indistinguishable from an empty one.
func TestSyncTextPtr(t *testing.T) {
	assert.Nil(t, syncTextPtr(""), "an absent Plaid field must store as NULL, not empty string")
	v := syncTextPtr("TRANSFER_IN")
	require.NotNil(t, v)
	assert.Equal(t, "TRANSFER_IN", *v)
}
