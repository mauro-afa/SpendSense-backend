package service

import (
	"context"
	"github.com/BeWellSpent/wellspent-backend/internal/category"
	"testing"
	"time"

	"github.com/BeWellSpent/wellspent-backend/internal/apperr"
	db "github.com/BeWellSpent/wellspent-backend/internal/sqlc"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── Mock transaction repo ─────────────────────────────────────────────────────

type mockTransactionRepo struct {
	list                                func(context.Context, db.ListTransactionsParams) ([]db.Transaction, error)
	listFixedRecurring                  func(context.Context, uuid.UUID) ([]db.Transaction, error)
	getByID                             func(context.Context, uuid.UUID) (db.Transaction, error)
	create                              func(context.Context, db.CreateTransactionParams) (db.Transaction, error)
	update                              func(context.Context, db.UpdateTransactionParams) (db.Transaction, error)
	delete                              func(context.Context, db.DeleteTransactionParams) error
	getCategory                         func(context.Context, int32) (db.GetCategoryRow, error)
	listCategories                      func(context.Context, uuid.UUID) ([]db.ListCategoriesRow, error)
	listCategoriesForBudget             func(context.Context, uuid.UUID, uuid.UUID) ([]db.ListCategoriesRow, error)
	createCategory                      func(context.Context, db.CreateCategoryParams) (db.CreateCategoryRow, error)
	updateCategory                      func(context.Context, db.UpdateCategoryParams) (db.UpdateCategoryRow, error)
	updateSystemCategoryColor           func(context.Context, db.UpdateSystemCategoryColorParams) (db.UpdateSystemCategoryColorRow, error)
	deleteCategoryAndReassign           func(context.Context, db.DeleteCategoryAndReassignParams) error
	listPaymentMethods                  func(context.Context, uuid.UUID) ([]db.ListPaymentMethodsRow, error)
	createPaymentMethod                 func(context.Context, db.CreatePaymentMethodParams) (db.PaymentMethod, error)
	updatePaymentMethod                 func(context.Context, db.UpdatePaymentMethodParams) (db.PaymentMethod, error)
	getPaymentMethod                    func(context.Context, uuid.UUID) (db.PaymentMethod, error)
	deletePaymentMethodAndReassign      func(context.Context, db.DeletePaymentMethodAndReassignParams) error
	listPaymentMethodIDsByBudgetProfile func(context.Context, uuid.UUID) ([]uuid.UUID, error)
	deletePaymentMethodsByIDs           func(context.Context, []uuid.UUID) error
	deleteSavingsSourceTransactions     func(context.Context, db.DeleteSavingsSourceTransactionsParams) error
	markAsPaid                          func(context.Context, db.MarkTransactionAsPaidParams) (db.Transaction, error)
	unmarkAsPaid                        func(context.Context, db.UnmarkTransactionAsPaidParams) (db.Transaction, error)
	setExcluded                         func(context.Context, db.SetTransactionExcludedParams) (db.Transaction, error)
	setInstallmentPlan                  func(context.Context, db.SetTransactionInstallmentPlanParams) (db.Transaction, error)
	clearInstallmentPlan                func(context.Context, db.ClearTransactionInstallmentPlanParams) (db.Transaction, error)
	listByFixedExpense                  func(context.Context, uuid.UUID) ([]db.Transaction, error)
	countCarried                        func(context.Context, db.CountCarriedTransactionsParams) (int64, error)
	deleteByFixedExpense                func(context.Context, uuid.UUID) error
	createPaymentMethodFromPlaid        func(context.Context, db.CreatePaymentMethodFromPlaidParams) (db.PaymentMethod, error)
	getPaymentMethodByPlaidAccountID    func(context.Context, string) (db.PaymentMethod, error)
	getPaymentMethodByUserAndName       func(context.Context, uuid.UUID, string) (db.PaymentMethod, error)
	updatePaymentMethodPlaidAccountID   func(context.Context, uuid.UUID, string) error
	listActivePaymentMethodsByPlaidItem func(context.Context, uuid.UUID) ([]db.PaymentMethod, error)
	deactivatePaymentMethod             func(context.Context, uuid.UUID) error
	createTransactionFromPlaid          func(context.Context, db.CreateTransactionFromPlaidParams) (db.Transaction, error)
	existsTransactionByPlaidID          func(context.Context, *string) (bool, error)
	getTransactionByPlaidID             func(context.Context, *string) (db.Transaction, error)
	repointTransactionPlaidID           func(context.Context, db.RepointTransactionPlaidIDParams) (db.Transaction, error)
	listSystemCategories                func(context.Context) (map[category.Key]int32, error)
}

// ── Mock fixed expense repo ───────────────────────────────────────────────────

type mockFixedExpenseRepo struct {
	create                     func(context.Context, db.CreateFixedExpenseParams) (db.FixedExpense, error)
	getByID                    func(context.Context, uuid.UUID) (db.FixedExpense, error)
	list                       func(context.Context, uuid.UUID) ([]db.FixedExpense, error)
	update                     func(context.Context, db.UpdateFixedExpenseParams) (db.FixedExpense, error)
	updateFromPayment          func(context.Context, db.UpdateFixedExpenseFromPaymentParams) error
	deactivate                 func(context.Context, db.DeactivateFixedExpenseParams) error
	getUnpaidTransaction       func(context.Context, db.GetUnpaidTransactionByFixedExpenseParams) (db.Transaction, error)
	getUnpaidTransactionInPer  func(context.Context, db.GetUnpaidTransactionByFixedExpenseInPeriodParams) (db.Transaction, error)
	getTransaction             func(context.Context, db.GetTransactionByFixedExpenseParams) (db.Transaction, error)
	deleteUnpaidTransactions   func(context.Context, db.DeleteUnpaidTransactionByFixedExpenseParams) error
	updateTransactionFromFixed func(context.Context, db.UpdateTransactionFromFixedExpenseParams) error
	updatePaidTxFromFixed      func(context.Context, db.UpdatePaidTransactionFromFixedExpenseParams) error
	hasTransactionInMonth      func(context.Context, db.FixedExpenseHasTransactionInMonthParams) (bool, error)
	hasTransactionOnDate       func(context.Context, db.FixedExpenseHasTransactionOnDateParams) (bool, error)
}

func (m *mockFixedExpenseRepo) Create(ctx context.Context, arg db.CreateFixedExpenseParams) (db.FixedExpense, error) {
	if m.create != nil {
		return m.create(ctx, arg)
	}
	return db.FixedExpense{ID: uuid.New(), BudgetProfileID: arg.BudgetProfileID, Name: arg.Name}, nil
}
func (m *mockFixedExpenseRepo) GetByID(ctx context.Context, id uuid.UUID) (db.FixedExpense, error) {
	if m.getByID != nil {
		return m.getByID(ctx, id)
	}
	return db.FixedExpense{ID: id}, nil
}
func (m *mockFixedExpenseRepo) List(ctx context.Context, budgetProfileID uuid.UUID) ([]db.FixedExpense, error) {
	if m.list != nil {
		return m.list(ctx, budgetProfileID)
	}
	return nil, nil
}
func (m *mockFixedExpenseRepo) Update(ctx context.Context, arg db.UpdateFixedExpenseParams) (db.FixedExpense, error) {
	if m.update != nil {
		return m.update(ctx, arg)
	}
	return db.FixedExpense{ID: arg.ID, Name: arg.Name}, nil
}
func (m *mockFixedExpenseRepo) UpdateFromPayment(ctx context.Context, arg db.UpdateFixedExpenseFromPaymentParams) error {
	if m.updateFromPayment != nil {
		return m.updateFromPayment(ctx, arg)
	}
	return nil
}
func (m *mockFixedExpenseRepo) Deactivate(ctx context.Context, arg db.DeactivateFixedExpenseParams) error {
	if m.deactivate != nil {
		return m.deactivate(ctx, arg)
	}
	return nil
}
func (m *mockFixedExpenseRepo) GetUnpaidTransaction(ctx context.Context, arg db.GetUnpaidTransactionByFixedExpenseParams) (db.Transaction, error) {
	if m.getUnpaidTransaction != nil {
		return m.getUnpaidTransaction(ctx, arg)
	}
	return db.Transaction{}, nil
}
func (m *mockFixedExpenseRepo) GetUnpaidTransactionInPeriod(ctx context.Context, arg db.GetUnpaidTransactionByFixedExpenseInPeriodParams) (db.Transaction, error) {
	if m.getUnpaidTransactionInPer != nil {
		return m.getUnpaidTransactionInPer(ctx, arg)
	}
	return db.Transaction{}, nil
}
func (m *mockFixedExpenseRepo) GetTransaction(ctx context.Context, arg db.GetTransactionByFixedExpenseParams) (db.Transaction, error) {
	if m.getTransaction != nil {
		return m.getTransaction(ctx, arg)
	}
	return db.Transaction{}, nil
}
func (m *mockFixedExpenseRepo) DeleteUnpaidTransactions(ctx context.Context, arg db.DeleteUnpaidTransactionByFixedExpenseParams) error {
	if m.deleteUnpaidTransactions != nil {
		return m.deleteUnpaidTransactions(ctx, arg)
	}
	return nil
}
func (m *mockFixedExpenseRepo) UpdateTransactionFromFixedExpense(ctx context.Context, arg db.UpdateTransactionFromFixedExpenseParams) error {
	if m.updateTransactionFromFixed != nil {
		return m.updateTransactionFromFixed(ctx, arg)
	}
	return nil
}
func (m *mockFixedExpenseRepo) UpdatePaidTransactionFromFixedExpense(ctx context.Context, arg db.UpdatePaidTransactionFromFixedExpenseParams) error {
	if m.updatePaidTxFromFixed != nil {
		return m.updatePaidTxFromFixed(ctx, arg)
	}
	return nil
}
func (m *mockFixedExpenseRepo) HasTransactionInMonth(ctx context.Context, arg db.FixedExpenseHasTransactionInMonthParams) (bool, error) {
	if m.hasTransactionInMonth != nil {
		return m.hasTransactionInMonth(ctx, arg)
	}
	return false, nil
}
func (m *mockFixedExpenseRepo) HasTransactionOnDate(ctx context.Context, arg db.FixedExpenseHasTransactionOnDateParams) (bool, error) {
	if m.hasTransactionOnDate != nil {
		return m.hasTransactionOnDate(ctx, arg)
	}
	return false, nil
}

// ── Mock expense allocation repo ──────────────────────────────────────────────

type mockExpenseAllocationRepo struct {
	list   func(context.Context, uuid.UUID) ([]db.ExpenseAllocation, error)
	upsert func(context.Context, db.UpsertExpenseAllocationParams) (db.ExpenseAllocation, error)
	del    func(context.Context, db.DeleteExpenseAllocationParams) error
}

func (m *mockExpenseAllocationRepo) List(ctx context.Context, profileID uuid.UUID) ([]db.ExpenseAllocation, error) {
	if m.list != nil {
		return m.list(ctx, profileID)
	}
	return nil, nil
}
func (m *mockExpenseAllocationRepo) Upsert(ctx context.Context, arg db.UpsertExpenseAllocationParams) (db.ExpenseAllocation, error) {
	if m.upsert != nil {
		return m.upsert(ctx, arg)
	}
	return db.ExpenseAllocation{}, nil
}
func (m *mockExpenseAllocationRepo) Delete(ctx context.Context, arg db.DeleteExpenseAllocationParams) error {
	if m.del != nil {
		return m.del(ctx, arg)
	}
	return nil
}

func (m *mockTransactionRepo) List(ctx context.Context, arg db.ListTransactionsParams) ([]db.Transaction, error) {
	if m.list != nil {
		return m.list(ctx, arg)
	}
	return nil, nil
}
func (m *mockTransactionRepo) ListFixedRecurring(ctx context.Context, id uuid.UUID) ([]db.Transaction, error) {
	if m.listFixedRecurring != nil {
		return m.listFixedRecurring(ctx, id)
	}
	return nil, nil
}
func (m *mockTransactionRepo) GetByID(ctx context.Context, id uuid.UUID) (db.Transaction, error) {
	if m.getByID != nil {
		return m.getByID(ctx, id)
	}
	return db.Transaction{}, apperr.NotFound("transaction", id.String())
}
func (m *mockTransactionRepo) Create(ctx context.Context, arg db.CreateTransactionParams) (db.Transaction, error) {
	if m.create != nil {
		return m.create(ctx, arg)
	}
	return db.Transaction{}, nil
}
func (m *mockTransactionRepo) Update(ctx context.Context, arg db.UpdateTransactionParams) (db.Transaction, error) {
	if m.update != nil {
		return m.update(ctx, arg)
	}
	return db.Transaction{}, nil
}
func (m *mockTransactionRepo) Delete(ctx context.Context, arg db.DeleteTransactionParams) error {
	if m.delete != nil {
		return m.delete(ctx, arg)
	}
	return nil
}
func (m *mockTransactionRepo) GetCategory(ctx context.Context, id int32) (db.GetCategoryRow, error) {
	if m.getCategory != nil {
		return m.getCategory(ctx, id)
	}
	return db.GetCategoryRow{}, apperr.NotFound("category", "0")
}
func (m *mockTransactionRepo) ListCategories(ctx context.Context, userID uuid.UUID) ([]db.ListCategoriesRow, error) {
	if m.listCategories != nil {
		return m.listCategories(ctx, userID)
	}
	return nil, nil
}
func (m *mockTransactionRepo) ListCategoriesForBudget(ctx context.Context, userID uuid.UUID, budgetProfileID uuid.UUID) ([]db.ListCategoriesRow, error) {
	if m.listCategoriesForBudget != nil {
		return m.listCategoriesForBudget(ctx, userID, budgetProfileID)
	}
	return nil, nil
}
func (m *mockTransactionRepo) CreateCategory(ctx context.Context, arg db.CreateCategoryParams) (db.CreateCategoryRow, error) {
	if m.createCategory != nil {
		return m.createCategory(ctx, arg)
	}
	return db.CreateCategoryRow{}, nil
}
func (m *mockTransactionRepo) UpdateCategory(ctx context.Context, arg db.UpdateCategoryParams) (db.UpdateCategoryRow, error) {
	if m.updateCategory != nil {
		return m.updateCategory(ctx, arg)
	}
	return db.UpdateCategoryRow{}, nil
}
func (m *mockTransactionRepo) UpdateSystemCategoryColor(ctx context.Context, arg db.UpdateSystemCategoryColorParams) (db.UpdateSystemCategoryColorRow, error) {
	if m.updateSystemCategoryColor != nil {
		return m.updateSystemCategoryColor(ctx, arg)
	}
	return db.UpdateSystemCategoryColorRow{}, nil
}
func (m *mockTransactionRepo) DeleteCategoryAndReassign(ctx context.Context, arg db.DeleteCategoryAndReassignParams) error {
	if m.deleteCategoryAndReassign != nil {
		return m.deleteCategoryAndReassign(ctx, arg)
	}
	return nil
}
func (m *mockTransactionRepo) ListPaymentMethods(ctx context.Context, id uuid.UUID) ([]db.ListPaymentMethodsRow, error) {
	if m.listPaymentMethods != nil {
		return m.listPaymentMethods(ctx, id)
	}
	return nil, nil
}
func (m *mockTransactionRepo) CreatePaymentMethod(ctx context.Context, arg db.CreatePaymentMethodParams) (db.PaymentMethod, error) {
	if m.createPaymentMethod != nil {
		return m.createPaymentMethod(ctx, arg)
	}
	return db.PaymentMethod{}, nil
}
func (m *mockTransactionRepo) UpdatePaymentMethod(ctx context.Context, arg db.UpdatePaymentMethodParams) (db.PaymentMethod, error) {
	if m.updatePaymentMethod != nil {
		return m.updatePaymentMethod(ctx, arg)
	}
	return db.PaymentMethod{}, nil
}
func (m *mockTransactionRepo) GetPaymentMethod(ctx context.Context, id uuid.UUID) (db.PaymentMethod, error) {
	if m.getPaymentMethod != nil {
		return m.getPaymentMethod(ctx, id)
	}
	return db.PaymentMethod{}, nil
}
func (m *mockTransactionRepo) DeletePaymentMethodAndReassign(ctx context.Context, arg db.DeletePaymentMethodAndReassignParams) error {
	if m.deletePaymentMethodAndReassign != nil {
		return m.deletePaymentMethodAndReassign(ctx, arg)
	}
	return nil
}
func (m *mockTransactionRepo) ListPaymentMethodIDsByBudgetProfile(ctx context.Context, budgetProfileID uuid.UUID) ([]uuid.UUID, error) {
	if m.listPaymentMethodIDsByBudgetProfile != nil {
		return m.listPaymentMethodIDsByBudgetProfile(ctx, budgetProfileID)
	}
	return nil, nil
}
func (m *mockTransactionRepo) DeletePaymentMethodsByIDs(ctx context.Context, ids []uuid.UUID) error {
	if m.deletePaymentMethodsByIDs != nil {
		return m.deletePaymentMethodsByIDs(ctx, ids)
	}
	return nil
}
func (m *mockTransactionRepo) DeleteSavingsSourceTransactions(ctx context.Context, arg db.DeleteSavingsSourceTransactionsParams) error {
	if m.deleteSavingsSourceTransactions != nil {
		return m.deleteSavingsSourceTransactions(ctx, arg)
	}
	return nil
}
func (m *mockTransactionRepo) MarkAsPaid(ctx context.Context, arg db.MarkTransactionAsPaidParams) (db.Transaction, error) {
	if m.markAsPaid != nil {
		return m.markAsPaid(ctx, arg)
	}
	return db.Transaction{}, nil
}

func (m *mockTransactionRepo) UnmarkAsPaid(ctx context.Context, arg db.UnmarkTransactionAsPaidParams) (db.Transaction, error) {
	if m.unmarkAsPaid != nil {
		return m.unmarkAsPaid(ctx, arg)
	}
	return db.Transaction{}, nil
}

func (m *mockTransactionRepo) SetExcluded(ctx context.Context, arg db.SetTransactionExcludedParams) (db.Transaction, error) {
	if m.setExcluded != nil {
		return m.setExcluded(ctx, arg)
	}
	return db.Transaction{}, nil
}

func (m *mockTransactionRepo) SetInstallmentPlan(ctx context.Context, arg db.SetTransactionInstallmentPlanParams) (db.Transaction, error) {
	if m.setInstallmentPlan != nil {
		return m.setInstallmentPlan(ctx, arg)
	}
	return db.Transaction{}, nil
}

func (m *mockTransactionRepo) ClearInstallmentPlan(ctx context.Context, arg db.ClearTransactionInstallmentPlanParams) (db.Transaction, error) {
	if m.clearInstallmentPlan != nil {
		return m.clearInstallmentPlan(ctx, arg)
	}
	return db.Transaction{}, nil
}

func (m *mockTransactionRepo) ListByFixedExpense(ctx context.Context, id uuid.UUID) ([]db.Transaction, error) {
	if m.listByFixedExpense != nil {
		return m.listByFixedExpense(ctx, id)
	}
	return nil, nil
}

func (m *mockTransactionRepo) CountCarried(ctx context.Context, arg db.CountCarriedTransactionsParams) (int64, error) {
	if m.countCarried != nil {
		return m.countCarried(ctx, arg)
	}
	return 0, nil
}

func (m *mockTransactionRepo) DeleteByFixedExpense(ctx context.Context, id uuid.UUID) error {
	if m.deleteByFixedExpense != nil {
		return m.deleteByFixedExpense(ctx, id)
	}
	return nil
}

func (m *mockTransactionRepo) CreateTransactionFromPlaid(ctx context.Context, arg db.CreateTransactionFromPlaidParams) (db.Transaction, error) {
	if m.createTransactionFromPlaid != nil {
		return m.createTransactionFromPlaid(ctx, arg)
	}
	return db.Transaction{}, nil
}

func (m *mockTransactionRepo) ExistsTransactionByPlaidID(ctx context.Context, plaidTransactionID *string) (bool, error) {
	if m.existsTransactionByPlaidID != nil {
		return m.existsTransactionByPlaidID(ctx, plaidTransactionID)
	}
	return false, nil
}

func (m *mockTransactionRepo) UpdateTransactionFromPlaid(ctx context.Context, arg db.UpdateTransactionFromPlaidParams) error {
	return nil
}

func (m *mockTransactionRepo) GetTransactionByPlaidID(ctx context.Context, plaidTransactionID *string) (db.Transaction, error) {
	if m.getTransactionByPlaidID != nil {
		return m.getTransactionByPlaidID(ctx, plaidTransactionID)
	}
	id := ""
	if plaidTransactionID != nil {
		id = *plaidTransactionID
	}
	return db.Transaction{}, apperr.NotFound("transaction", id)
}

func (m *mockTransactionRepo) RepointTransactionPlaidID(ctx context.Context, arg db.RepointTransactionPlaidIDParams) (db.Transaction, error) {
	if m.repointTransactionPlaidID != nil {
		return m.repointTransactionPlaidID(ctx, arg)
	}
	return db.Transaction{}, nil
}

func (m *mockTransactionRepo) DeleteTransactionByPlaidID(ctx context.Context, plaidTransactionID *string) error {
	return nil
}

func (m *mockTransactionRepo) CreatePaymentMethodFromPlaid(ctx context.Context, arg db.CreatePaymentMethodFromPlaidParams) (db.PaymentMethod, error) {
	if m.createPaymentMethodFromPlaid != nil {
		return m.createPaymentMethodFromPlaid(ctx, arg)
	}
	return db.PaymentMethod{}, nil
}

func (m *mockTransactionRepo) GetPaymentMethodByPlaidAccountID(ctx context.Context, plaidAccountID string) (db.PaymentMethod, error) {
	if m.getPaymentMethodByPlaidAccountID != nil {
		return m.getPaymentMethodByPlaidAccountID(ctx, plaidAccountID)
	}
	return db.PaymentMethod{}, apperr.NotFound("payment_method", plaidAccountID)
}

func (m *mockTransactionRepo) GetPaymentMethodByUserAndName(ctx context.Context, userID uuid.UUID, name string) (db.PaymentMethod, error) {
	if m.getPaymentMethodByUserAndName != nil {
		return m.getPaymentMethodByUserAndName(ctx, userID, name)
	}
	return db.PaymentMethod{}, apperr.NotFound("payment_method", "name")
}

func (m *mockTransactionRepo) UpdatePaymentMethodPlaidAccountID(ctx context.Context, id uuid.UUID, plaidAccountID string) error {
	if m.updatePaymentMethodPlaidAccountID != nil {
		return m.updatePaymentMethodPlaidAccountID(ctx, id, plaidAccountID)
	}
	return nil
}

func (m *mockTransactionRepo) ListActivePaymentMethodsByPlaidItem(ctx context.Context, plaidItemID uuid.UUID) ([]db.PaymentMethod, error) {
	if m.listActivePaymentMethodsByPlaidItem != nil {
		return m.listActivePaymentMethodsByPlaidItem(ctx, plaidItemID)
	}
	return nil, nil
}

func (m *mockTransactionRepo) DeactivatePaymentMethod(ctx context.Context, id uuid.UUID) error {
	if m.deactivatePaymentMethod != nil {
		return m.deactivatePaymentMethod(ctx, id)
	}
	return nil
}

func (m *mockTransactionRepo) ListSystemCategories(ctx context.Context) (map[category.Key]int32, error) {
	if m.listSystemCategories != nil {
		return m.listSystemCategories(ctx)
	}
	return map[category.Key]int32{}, nil
}

// ── mockTransactionReviewRepo ─────────────────────────────────────────────────

type mockTransactionReviewRepo struct {
	create                  func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, float64) (db.TransactionReview, error)
	upsert                  func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, float64) (db.TransactionReview, error)
	list                    func(context.Context, uuid.UUID) ([]db.ListTransactionReviewsRow, error)
	getByID                 func(context.Context, uuid.UUID) (db.TransactionReview, error)
	updateStatus            func(context.Context, uuid.UUID, string) error
	getConfirmedByMatchedTx func(context.Context, uuid.UUID) (db.TransactionReview, error)
	getByTransactionID      func(context.Context, uuid.UUID) (db.TransactionReview, error)
	resetByMatchedTx        func(context.Context, uuid.UUID) error
	createAlias             func(context.Context, uuid.UUID, string) error
	deleteAlias             func(context.Context, uuid.UUID, string) error
	listAliases             func(context.Context, uuid.UUID) ([]string, error)
	getFixedExpenseByAlias  func(context.Context, string, uuid.UUID) (db.GetFixedExpenseByAliasRow, error)
}

func (m *mockTransactionReviewRepo) Create(ctx context.Context, periodID, transactionID, matchedTransactionID uuid.UUID, score float64) (db.TransactionReview, error) {
	if m.create != nil {
		return m.create(ctx, periodID, transactionID, matchedTransactionID, score)
	}
	return db.TransactionReview{}, nil
}
func (m *mockTransactionReviewRepo) List(ctx context.Context, budgetProfileID uuid.UUID) ([]db.ListTransactionReviewsRow, error) {
	if m.list != nil {
		return m.list(ctx, budgetProfileID)
	}
	return nil, nil
}
func (m *mockTransactionReviewRepo) GetByID(ctx context.Context, id uuid.UUID) (db.TransactionReview, error) {
	if m.getByID != nil {
		return m.getByID(ctx, id)
	}
	return db.TransactionReview{}, apperr.NotFound("transaction_review", id.String())
}
func (m *mockTransactionReviewRepo) UpdateStatus(ctx context.Context, id uuid.UUID, status string) error {
	if m.updateStatus != nil {
		return m.updateStatus(ctx, id, status)
	}
	return nil
}
func (m *mockTransactionReviewRepo) GetConfirmedByMatchedTransaction(ctx context.Context, matchedTransactionID uuid.UUID) (db.TransactionReview, error) {
	if m.getConfirmedByMatchedTx != nil {
		return m.getConfirmedByMatchedTx(ctx, matchedTransactionID)
	}
	return db.TransactionReview{}, apperr.NotFound("transaction_review", "")
}
func (m *mockTransactionReviewRepo) GetByTransactionID(ctx context.Context, transactionID uuid.UUID) (db.TransactionReview, error) {
	if m.getByTransactionID != nil {
		return m.getByTransactionID(ctx, transactionID)
	}
	return db.TransactionReview{}, apperr.NotFound("transaction_review", "")
}
func (m *mockTransactionReviewRepo) ResetByMatchedTransaction(ctx context.Context, matchedTransactionID uuid.UUID) error {
	if m.resetByMatchedTx != nil {
		return m.resetByMatchedTx(ctx, matchedTransactionID)
	}
	return nil
}
func (m *mockTransactionReviewRepo) CreateAlias(ctx context.Context, fixedExpenseID uuid.UUID, alias string) error {
	if m.createAlias != nil {
		return m.createAlias(ctx, fixedExpenseID, alias)
	}
	return nil
}
func (m *mockTransactionReviewRepo) DeleteAlias(ctx context.Context, fixedExpenseID uuid.UUID, alias string) error {
	if m.deleteAlias != nil {
		return m.deleteAlias(ctx, fixedExpenseID, alias)
	}
	return nil
}
func (m *mockTransactionReviewRepo) ListAliases(ctx context.Context, fixedExpenseID uuid.UUID) ([]string, error) {
	if m.listAliases != nil {
		return m.listAliases(ctx, fixedExpenseID)
	}
	return nil, nil
}
func (m *mockTransactionReviewRepo) Upsert(ctx context.Context, periodID, transactionID, matchedTransactionID uuid.UUID, score float64) (db.TransactionReview, error) {
	if m.upsert != nil {
		return m.upsert(ctx, periodID, transactionID, matchedTransactionID, score)
	}
	return db.TransactionReview{}, nil
}
func (m *mockTransactionReviewRepo) GetFixedExpenseByAlias(ctx context.Context, alias string, budgetProfileID uuid.UUID) (db.GetFixedExpenseByAliasRow, error) {
	if m.getFixedExpenseByAlias != nil {
		return m.getFixedExpenseByAlias(ctx, alias, budgetProfileID)
	}
	return db.GetFixedExpenseByAliasRow{}, apperr.NotFound("fixed_expense_alias", "")
}

// ── UpdatePaymentMethod tests ─────────────────────────────────────────────────

func TestUpdatePaymentMethod_Success_OwnMethod(t *testing.T) {
	typeID := int32(2) // CREDIT
	methodID := uuid.New()
	userID := uuid.New()
	expected := db.PaymentMethod{
		ID:            methodID,
		Name:          "Chase Visa",
		PaymentTypeID: &typeID,
		UserID:        &userID,
	}

	svc := NewTransactionService(
		&mockTransactionRepo{
			getPaymentMethod: func(_ context.Context, id uuid.UUID) (db.PaymentMethod, error) {
				return db.PaymentMethod{ID: id, UserID: &userID}, nil
			},
			updatePaymentMethod: func(_ context.Context, arg db.UpdatePaymentMethodParams) (db.PaymentMethod, error) {
				assert.Equal(t, methodID, arg.ID)
				assert.Equal(t, "Chase Visa", arg.Name)
				return expected, nil
			},
		},
		&mockBudgetProfileRepo{},
		&mockExpenseAllocationRepo{},
		&mockFixedExpenseRepo{},
		&mockTransactionReviewRepo{},
	)

	result, err := svc.UpdatePaymentMethod(context.Background(), db.UpdatePaymentMethodParams{
		ID:   methodID,
		Name: "Chase Visa",
	}, userID)

	require.NoError(t, err)
	assert.Equal(t, expected.ID, result.ID)
	assert.Equal(t, expected.Name, result.Name)
	assert.Equal(t, expected.PaymentTypeID, result.PaymentTypeID)
}

func TestUpdatePaymentMethod_Success_AdminUpdatesCollaboratorMethod(t *testing.T) {
	typeID := int32(2) // CREDIT
	methodID := uuid.New()
	adminID := uuid.New()
	collaboratorPersonID := int32(7)
	profileID := uuid.New()
	expected := db.PaymentMethod{
		ID:            methodID,
		Name:          "Updated Name",
		PaymentTypeID: &typeID,
	}

	svc := NewTransactionService(
		&mockTransactionRepo{
			getPaymentMethod: func(_ context.Context, id uuid.UUID) (db.PaymentMethod, error) {
				return db.PaymentMethod{ID: id, BudgetPersonID: &collaboratorPersonID}, nil
			},
			updatePaymentMethod: func(_ context.Context, arg db.UpdatePaymentMethodParams) (db.PaymentMethod, error) {
				return expected, nil
			},
		},
		&mockBudgetProfileRepo{
			getPersonByID: func(_ context.Context, personID int32) (db.BudgetToProfileMapping, error) {
				return db.BudgetToProfileMapping{ID: personID, BudgetProfileID: profileID}, nil
			},
			getByID: func(_ context.Context, id uuid.UUID) (db.BudgetProfile, error) {
				return db.BudgetProfile{ID: id, UserID: adminID}, nil
			},
		},
		&mockExpenseAllocationRepo{},
		&mockFixedExpenseRepo{},
		&mockTransactionReviewRepo{},
	)

	result, err := svc.UpdatePaymentMethod(context.Background(), db.UpdatePaymentMethodParams{
		ID:   methodID,
		Name: "Updated Name",
	}, adminID)

	require.NoError(t, err)
	assert.Equal(t, expected.ID, result.ID)
}

func TestUpdatePaymentMethod_Forbidden_ViewerCannotUpdate(t *testing.T) {
	methodID := uuid.New()
	viewerID := uuid.New()
	personID := int32(5)
	profileID := uuid.New()
	ownerID := uuid.New()

	svc := NewTransactionService(
		&mockTransactionRepo{
			getPaymentMethod: func(_ context.Context, id uuid.UUID) (db.PaymentMethod, error) {
				return db.PaymentMethod{ID: id, BudgetPersonID: &personID}, nil
			},
		},
		&mockBudgetProfileRepo{
			getPersonByID: func(_ context.Context, pid int32) (db.BudgetToProfileMapping, error) {
				return db.BudgetToProfileMapping{ID: pid, BudgetProfileID: profileID}, nil
			},
			getByID: func(_ context.Context, id uuid.UUID) (db.BudgetProfile, error) {
				return db.BudgetProfile{ID: id, UserID: ownerID}, nil
			},
			getPersonByUserID: func(_ context.Context, profID, uid uuid.UUID) (db.BudgetToProfileMapping, error) {
				return db.BudgetToProfileMapping{Role: "viewer"}, nil
			},
		},
		&mockExpenseAllocationRepo{},
		&mockFixedExpenseRepo{},
		&mockTransactionReviewRepo{},
	)

	_, err := svc.UpdatePaymentMethod(context.Background(), db.UpdatePaymentMethodParams{
		ID:   methodID,
		Name: "Renamed",
	}, viewerID)

	require.Error(t, err)
	var forbidden *apperr.ForbiddenError
	require.ErrorAs(t, err, &forbidden)
}

func TestUpdatePaymentMethod_Forbidden_WhenNotOwnerOfUnattributedMethod(t *testing.T) {
	methodID := uuid.New()
	ownerID := uuid.New()
	otherUserID := uuid.New()

	svc := NewTransactionService(
		&mockTransactionRepo{
			getPaymentMethod: func(_ context.Context, id uuid.UUID) (db.PaymentMethod, error) {
				return db.PaymentMethod{ID: id, UserID: &ownerID}, nil
			},
		},
		&mockBudgetProfileRepo{},
		&mockExpenseAllocationRepo{},
		&mockFixedExpenseRepo{},
		&mockTransactionReviewRepo{},
	)

	_, err := svc.UpdatePaymentMethod(context.Background(), db.UpdatePaymentMethodParams{
		ID:   methodID,
		Name: "Renamed",
	}, otherUserID)

	require.Error(t, err)
	var forbidden *apperr.ForbiddenError
	require.ErrorAs(t, err, &forbidden)
}

func TestUpdatePaymentMethod_NotFound_WhenMethodMissing(t *testing.T) {
	svc := NewTransactionService(
		&mockTransactionRepo{
			getPaymentMethod: func(_ context.Context, id uuid.UUID) (db.PaymentMethod, error) {
				return db.PaymentMethod{}, apperr.NotFound("payment_method", id.String())
			},
		},
		&mockBudgetProfileRepo{},
		&mockExpenseAllocationRepo{},
		&mockFixedExpenseRepo{},
		&mockTransactionReviewRepo{},
	)

	_, err := svc.UpdatePaymentMethod(context.Background(), db.UpdatePaymentMethodParams{
		ID:   uuid.New(),
		Name: "Renamed",
	}, uuid.New())

	require.Error(t, err)
	var notFound *apperr.NotFoundError
	require.ErrorAs(t, err, &notFound)
	assert.Equal(t, "payment_method", notFound.Resource)
}

// ── CreateCategory tests ──────────────────────────────────────────────────────

func TestCreateCategory_Success(t *testing.T) {
	userID := uuid.New()

	svc := NewTransactionService(
		&mockTransactionRepo{
			createCategory: func(_ context.Context, arg db.CreateCategoryParams) (db.CreateCategoryRow, error) {
				assert.Equal(t, "Hobbies", arg.Name)
				assert.Equal(t, userID, arg.UserID)
				return db.CreateCategoryRow{ID: 42, Name: "Hobbies", IsSystem: false}, nil
			},
		},
		&mockBudgetProfileRepo{},
		&mockExpenseAllocationRepo{},
		&mockFixedExpenseRepo{},
		&mockTransactionReviewRepo{},
	)

	result, err := svc.CreateCategory(context.Background(), db.CreateCategoryParams{
		Name:   "Hobbies",
		UserID: userID,
	})

	require.NoError(t, err)
	assert.Equal(t, int32(42), result.ID)
	assert.Equal(t, "Hobbies", result.Name)
	assert.False(t, result.IsSystem)
}

// ── UpdateCategory tests ──────────────────────────────────────────────────────

func TestUpdateCategory_Success(t *testing.T) {
	userID := uuid.New()

	svc := NewTransactionService(
		&mockTransactionRepo{
			getCategory: func(_ context.Context, id int32) (db.GetCategoryRow, error) {
				return db.GetCategoryRow{ID: id, IsSystem: false}, nil
			},
			updateCategory: func(_ context.Context, arg db.UpdateCategoryParams) (db.UpdateCategoryRow, error) {
				assert.Equal(t, int32(10), arg.ID)
				assert.Equal(t, "Fun Money", arg.Name)
				assert.Equal(t, userID, arg.UserID)
				return db.UpdateCategoryRow{ID: 10, Name: "Fun Money", IsSystem: false}, nil
			},
		},
		&mockBudgetProfileRepo{},
		&mockExpenseAllocationRepo{},
		&mockFixedExpenseRepo{},
		&mockTransactionReviewRepo{},
	)

	result, err := svc.UpdateCategory(context.Background(), db.UpdateCategoryParams{
		ID:     10,
		Name:   "Fun Money",
		UserID: userID,
	})

	require.NoError(t, err)
	assert.Equal(t, "Fun Money", result.Name)
}

func TestUpdateCategory_NotFound(t *testing.T) {
	svc := NewTransactionService(
		&mockTransactionRepo{
			// getCategory returns NotFound (default) — service returns early
		},
		&mockBudgetProfileRepo{},
		&mockExpenseAllocationRepo{},
		&mockFixedExpenseRepo{},
		&mockTransactionReviewRepo{},
	)

	_, err := svc.UpdateCategory(context.Background(), db.UpdateCategoryParams{
		ID:     99,
		Name:   "Ghost",
		UserID: uuid.New(),
	})

	require.Error(t, err)
	var notFound *apperr.NotFoundError
	require.ErrorAs(t, err, &notFound)
	assert.Equal(t, "category", notFound.Resource)
}

func TestUpdateCategory_SystemColor_Success(t *testing.T) {
	svc := NewTransactionService(
		&mockTransactionRepo{
			getCategory: func(_ context.Context, id int32) (db.GetCategoryRow, error) {
				return db.GetCategoryRow{ID: id, Name: "Food", IsSystem: true}, nil
			},
			updateSystemCategoryColor: func(_ context.Context, arg db.UpdateSystemCategoryColorParams) (db.UpdateSystemCategoryColorRow, error) {
				assert.Equal(t, int32(5), arg.ID)
				assert.Equal(t, "#4caf50", arg.Color)
				return db.UpdateSystemCategoryColorRow{ID: 5, Name: "Food", IsSystem: true, Color: "#4caf50"}, nil
			},
		},
		&mockBudgetProfileRepo{},
		&mockExpenseAllocationRepo{},
		&mockFixedExpenseRepo{},
		&mockTransactionReviewRepo{},
	)

	result, err := svc.UpdateCategory(context.Background(), db.UpdateCategoryParams{
		ID:    5,
		Name:  "Food", // name is ignored for system categories
		Color: "#4caf50",
	})

	require.NoError(t, err)
	assert.Equal(t, "Food", result.Name)
	assert.Equal(t, "#4caf50", result.Color)
	assert.True(t, result.IsSystem)
}

func TestUpdateCategory_SystemColor_NotFound(t *testing.T) {
	svc := NewTransactionService(
		&mockTransactionRepo{
			getCategory: func(_ context.Context, id int32) (db.GetCategoryRow, error) {
				return db.GetCategoryRow{ID: id, IsSystem: true}, nil
			},
			updateSystemCategoryColor: func(_ context.Context, arg db.UpdateSystemCategoryColorParams) (db.UpdateSystemCategoryColorRow, error) {
				return db.UpdateSystemCategoryColorRow{}, apperr.NotFound("category", "99")
			},
		},
		&mockBudgetProfileRepo{},
		&mockExpenseAllocationRepo{},
		&mockFixedExpenseRepo{},
		&mockTransactionReviewRepo{},
	)

	_, err := svc.UpdateCategory(context.Background(), db.UpdateCategoryParams{
		ID:    99,
		Color: "#ff0000",
	})

	require.Error(t, err)
	var notFound *apperr.NotFoundError
	require.ErrorAs(t, err, &notFound)
}

// ── DeleteTransaction tests ───────────────────────────────────────────────────

func TestDeleteTransaction_Success(t *testing.T) {
	userID := uuid.New()
	profileID := uuid.New()
	periodID := uuid.New()
	txID := uuid.New()
	deleteCalled := false

	svc := NewTransactionService(
		&mockTransactionRepo{
			getByID: func(_ context.Context, _ uuid.UUID) (db.Transaction, error) {
				return db.Transaction{ID: txID, BudgetPeriodID: &periodID}, nil
			},
			delete: func(_ context.Context, arg db.DeleteTransactionParams) error {
				deleteCalled = true
				assert.Equal(t, txID, arg.ID)
				return nil
			},
		},
		&mockBudgetProfileRepo{
			getByID: func(_ context.Context, _ uuid.UUID) (db.BudgetProfile, error) {
				return db.BudgetProfile{ID: profileID, UserID: userID}, nil
			},
			getPeriodByID: func(_ context.Context, id uuid.UUID) (db.BudgetPeriod, error) {
				return db.BudgetPeriod{ID: id, BudgetProfileID: profileID, IsArchived: false}, nil
			},
		},
		&mockExpenseAllocationRepo{},
		&mockFixedExpenseRepo{},
		&mockTransactionReviewRepo{},
	)

	err := svc.Delete(context.Background(), txID, userID)
	require.NoError(t, err)
	assert.True(t, deleteCalled)
}

func TestDeleteTransaction_Invalid_WhenPlaidImported(t *testing.T) {
	userID := uuid.New()
	profileID := uuid.New()
	periodID := uuid.New()
	txID := uuid.New()
	plaidID := "plaid-tx-123"

	svc := NewTransactionService(
		&mockTransactionRepo{
			getByID: func(_ context.Context, _ uuid.UUID) (db.Transaction, error) {
				return db.Transaction{ID: txID, BudgetPeriodID: &periodID, PlaidTransactionID: &plaidID}, nil
			},
			delete: func(_ context.Context, _ db.DeleteTransactionParams) error {
				t.Fatal("delete should not be called for a Plaid-imported transaction")
				return nil
			},
		},
		&mockBudgetProfileRepo{
			getByID: func(_ context.Context, _ uuid.UUID) (db.BudgetProfile, error) {
				return db.BudgetProfile{ID: profileID, UserID: userID}, nil
			},
			getPeriodByID: func(_ context.Context, id uuid.UUID) (db.BudgetPeriod, error) {
				return db.BudgetPeriod{ID: id, BudgetProfileID: profileID, IsArchived: false}, nil
			},
		},
		&mockExpenseAllocationRepo{},
		&mockFixedExpenseRepo{},
		&mockTransactionReviewRepo{},
	)

	err := svc.Delete(context.Background(), txID, userID)
	require.Error(t, err)
	var invalid *apperr.ValidationError
	require.ErrorAs(t, err, &invalid)
}

// ── DeleteCategory tests ──────────────────────────────────────────────────────

func TestDeleteCategory_Success(t *testing.T) {
	userID := uuid.New()
	catID := int32(5)
	replacementID := int32(1)

	svc := NewTransactionService(
		&mockTransactionRepo{
			getCategory: func(_ context.Context, id int32) (db.GetCategoryRow, error) {
				if id == catID {
					return db.GetCategoryRow{ID: catID, Name: "Old", IsSystem: false, UserID: &userID}, nil
				}
				return db.GetCategoryRow{ID: replacementID, Name: "Entertainment", IsSystem: true}, nil
			},
			deleteCategoryAndReassign: func(_ context.Context, arg db.DeleteCategoryAndReassignParams) error {
				assert.Equal(t, catID, arg.ID)
				assert.Equal(t, userID, arg.UserID)
				assert.Equal(t, &replacementID, arg.ReplacementID)
				return nil
			},
		},
		&mockBudgetProfileRepo{},
		&mockExpenseAllocationRepo{},
		&mockFixedExpenseRepo{},
		&mockTransactionReviewRepo{},
	)

	err := svc.DeleteCategory(context.Background(), catID, replacementID, userID)
	require.NoError(t, err)
}

func TestDeleteCategory_Forbidden_WhenSystemCategory(t *testing.T) {
	userID := uuid.New()

	svc := NewTransactionService(
		&mockTransactionRepo{
			getCategory: func(_ context.Context, id int32) (db.GetCategoryRow, error) {
				return db.GetCategoryRow{ID: id, Name: "Entertainment", IsSystem: true}, nil
			},
		},
		&mockBudgetProfileRepo{},
		&mockExpenseAllocationRepo{},
		&mockFixedExpenseRepo{},
		&mockTransactionReviewRepo{},
	)

	err := svc.DeleteCategory(context.Background(), 1, 2, userID)
	require.Error(t, err)
	var forbidden *apperr.ForbiddenError
	require.ErrorAs(t, err, &forbidden)
}

func TestDeleteCategory_Forbidden_WhenNotOwner(t *testing.T) {
	userID := uuid.New()
	otherUserID := uuid.New()
	catID := int32(5)

	svc := NewTransactionService(
		&mockTransactionRepo{
			getCategory: func(_ context.Context, id int32) (db.GetCategoryRow, error) {
				return db.GetCategoryRow{ID: catID, Name: "Old", IsSystem: false, UserID: &otherUserID}, nil
			},
		},
		&mockBudgetProfileRepo{},
		&mockExpenseAllocationRepo{},
		&mockFixedExpenseRepo{},
		&mockTransactionReviewRepo{},
	)

	err := svc.DeleteCategory(context.Background(), catID, 1, userID)
	require.Error(t, err)
	var forbidden *apperr.ForbiddenError
	require.ErrorAs(t, err, &forbidden)
}

func TestDeleteCategory_NotFound_WhenCategoryMissing(t *testing.T) {
	svc := NewTransactionService(
		&mockTransactionRepo{
			getCategory: func(_ context.Context, id int32) (db.GetCategoryRow, error) {
				return db.GetCategoryRow{}, apperr.NotFound("category", "99")
			},
		},
		&mockBudgetProfileRepo{},
		&mockExpenseAllocationRepo{},
		&mockFixedExpenseRepo{},
		&mockTransactionReviewRepo{},
	)

	err := svc.DeleteCategory(context.Background(), 99, 1, uuid.New())
	require.Error(t, err)
	var notFound *apperr.NotFoundError
	require.ErrorAs(t, err, &notFound)
}

// ── DeletePaymentMethod tests ─────────────────────────────────────────────────

func TestDeletePaymentMethod_Success(t *testing.T) {
	userID := uuid.New()
	methodID := uuid.New()
	replacementID := uuid.New()
	profileID := uuid.New()

	svc := NewTransactionService(
		&mockTransactionRepo{
			getPaymentMethod: func(_ context.Context, id uuid.UUID) (db.PaymentMethod, error) {
				return db.PaymentMethod{ID: id, Name: "method"}, nil
			},
			deletePaymentMethodAndReassign: func(_ context.Context, arg db.DeletePaymentMethodAndReassignParams) error {
				assert.Equal(t, methodID, arg.ID)
				assert.Equal(t, replacementID, arg.ReplacementID)
				assert.Equal(t, profileID, arg.BudgetProfileID)
				return nil
			},
		},
		&mockBudgetProfileRepo{
			getByID: func(_ context.Context, id uuid.UUID) (db.BudgetProfile, error) {
				return db.BudgetProfile{ID: id, UserID: userID}, nil
			},
		},
		&mockExpenseAllocationRepo{},
		&mockFixedExpenseRepo{},
		&mockTransactionReviewRepo{},
	)

	err := svc.DeletePaymentMethod(context.Background(), methodID, replacementID, profileID, userID)
	require.NoError(t, err)
}

func TestDeletePaymentMethod_Forbidden_WhenViewer(t *testing.T) {
	viewerID := uuid.New()
	ownerID := uuid.New()
	profileID := uuid.New()

	svc := NewTransactionService(
		&mockTransactionRepo{},
		&mockBudgetProfileRepo{
			getByID: func(_ context.Context, id uuid.UUID) (db.BudgetProfile, error) {
				return db.BudgetProfile{ID: id, UserID: ownerID}, nil
			},
			getPersonByUserID: func(_ context.Context, profID, uid uuid.UUID) (db.BudgetToProfileMapping, error) {
				return db.BudgetToProfileMapping{Role: "viewer"}, nil
			},
		},
		&mockExpenseAllocationRepo{},
		&mockFixedExpenseRepo{},
		&mockTransactionReviewRepo{},
	)

	err := svc.DeletePaymentMethod(context.Background(), uuid.New(), uuid.New(), profileID, viewerID)
	require.Error(t, err)
	var forbidden *apperr.ForbiddenError
	require.ErrorAs(t, err, &forbidden)
}

func TestDeletePaymentMethod_NotFound_WhenMethodMissing(t *testing.T) {
	userID := uuid.New()
	profileID := uuid.New()

	svc := NewTransactionService(
		&mockTransactionRepo{
			getPaymentMethod: func(_ context.Context, id uuid.UUID) (db.PaymentMethod, error) {
				return db.PaymentMethod{}, apperr.NotFound("payment_method", id.String())
			},
		},
		&mockBudgetProfileRepo{
			getByID: func(_ context.Context, id uuid.UUID) (db.BudgetProfile, error) {
				return db.BudgetProfile{ID: id, UserID: userID}, nil
			},
		},
		&mockExpenseAllocationRepo{},
		&mockFixedExpenseRepo{},
		&mockTransactionReviewRepo{},
	)

	err := svc.DeletePaymentMethod(context.Background(), uuid.New(), uuid.New(), profileID, userID)
	require.Error(t, err)
	var notFound *apperr.NotFoundError
	require.ErrorAs(t, err, &notFound)
}

// ── UnmarkTransactionAsPaid tests ─────────────────────────────────────────────

func TestUnmarkTransactionAsPaid_Success(t *testing.T) {
	userID := uuid.New()
	profileID := uuid.New()
	periodID := uuid.New()
	txID := uuid.New()

	svc := NewTransactionService(
		&mockTransactionRepo{
			unmarkAsPaid: func(_ context.Context, arg db.UnmarkTransactionAsPaidParams) (db.Transaction, error) {
				assert.Equal(t, txID, arg.ID)
				assert.Equal(t, periodID, arg.BudgetPeriodID)
				return db.Transaction{ID: txID, IsPaid: false}, nil
			},
		},
		&mockBudgetProfileRepo{
			getPeriodByID: func(_ context.Context, id uuid.UUID) (db.BudgetPeriod, error) {
				return db.BudgetPeriod{ID: id, BudgetProfileID: profileID}, nil
			},
			getByID: func(_ context.Context, _ uuid.UUID) (db.BudgetProfile, error) {
				return db.BudgetProfile{ID: profileID, UserID: userID}, nil
			},
		},
		&mockExpenseAllocationRepo{},
		&mockFixedExpenseRepo{},
		&mockTransactionReviewRepo{},
	)

	tx, err := svc.UnmarkTransactionAsPaid(context.Background(), txID, periodID, userID)
	require.NoError(t, err)
	assert.Equal(t, txID, tx.ID)
	assert.False(t, tx.IsPaid)
}

func TestUnmarkTransactionAsPaid_Forbidden_WhenNotOwner(t *testing.T) {
	profileID := uuid.New()
	periodID := uuid.New()

	svc := NewTransactionService(
		&mockTransactionRepo{},
		&mockBudgetProfileRepo{
			getPeriodByID: func(_ context.Context, id uuid.UUID) (db.BudgetPeriod, error) {
				return db.BudgetPeriod{ID: id, BudgetProfileID: profileID}, nil
			},
			getByID: func(_ context.Context, _ uuid.UUID) (db.BudgetProfile, error) {
				return db.BudgetProfile{ID: profileID, UserID: uuid.New()}, nil
			},
		},
		&mockExpenseAllocationRepo{},
		&mockFixedExpenseRepo{},
		&mockTransactionReviewRepo{},
	)

	_, err := svc.UnmarkTransactionAsPaid(context.Background(), uuid.New(), periodID, uuid.New())
	require.Error(t, err)
	var forbidden *apperr.ForbiddenError
	require.ErrorAs(t, err, &forbidden)
}

func TestSetTransactionExcluded_Success(t *testing.T) {
	userID := uuid.New()
	profileID := uuid.New()
	periodID := uuid.New()
	txID := uuid.New()

	svc := NewTransactionService(
		&mockTransactionRepo{
			setExcluded: func(_ context.Context, arg db.SetTransactionExcludedParams) (db.Transaction, error) {
				assert.Equal(t, txID, arg.ID)
				assert.Equal(t, periodID, arg.BudgetPeriodID)
				assert.True(t, arg.Excluded)
				return db.Transaction{ID: txID, IsExcluded: arg.Excluded}, nil
			},
		},
		&mockBudgetProfileRepo{
			getPeriodByID: func(_ context.Context, id uuid.UUID) (db.BudgetPeriod, error) {
				return db.BudgetPeriod{ID: id, BudgetProfileID: profileID}, nil
			},
			getByID: func(_ context.Context, _ uuid.UUID) (db.BudgetProfile, error) {
				return db.BudgetProfile{ID: profileID, UserID: userID}, nil
			},
		},
		&mockExpenseAllocationRepo{},
		&mockFixedExpenseRepo{},
		&mockTransactionReviewRepo{},
	)

	tx, err := svc.SetTransactionExcluded(context.Background(), txID, periodID, true, userID)
	require.NoError(t, err)
	assert.Equal(t, txID, tx.ID)
	assert.True(t, tx.IsExcluded)
}

func TestSetTransactionExcluded_Forbidden_WhenNotOwner(t *testing.T) {
	profileID := uuid.New()
	periodID := uuid.New()

	svc := NewTransactionService(
		&mockTransactionRepo{},
		&mockBudgetProfileRepo{
			getPeriodByID: func(_ context.Context, id uuid.UUID) (db.BudgetPeriod, error) {
				return db.BudgetPeriod{ID: id, BudgetProfileID: profileID}, nil
			},
			getByID: func(_ context.Context, _ uuid.UUID) (db.BudgetProfile, error) {
				return db.BudgetProfile{ID: profileID, UserID: uuid.New()}, nil
			},
		},
		&mockExpenseAllocationRepo{},
		&mockFixedExpenseRepo{},
		&mockTransactionReviewRepo{},
	)

	_, err := svc.SetTransactionExcluded(context.Background(), uuid.New(), periodID, true, uuid.New())
	require.Error(t, err)
	var forbidden *apperr.ForbiddenError
	require.ErrorAs(t, err, &forbidden)
}

// ── Role-based transaction access tests ──────────────────────────────────────

func TestListTransactions_CollaboratorAllowed(t *testing.T) {
	userID := uuid.New()
	profileID := uuid.New()
	periodID := uuid.New()

	svc := NewTransactionService(
		&mockTransactionRepo{
			list: func(_ context.Context, _ db.ListTransactionsParams) ([]db.Transaction, error) {
				return []db.Transaction{{ID: uuid.New()}}, nil
			},
		},
		&mockBudgetProfileRepo{
			getPeriodByID: func(_ context.Context, id uuid.UUID) (db.BudgetPeriod, error) {
				return db.BudgetPeriod{ID: id, BudgetProfileID: profileID}, nil
			},
			getByID: func(_ context.Context, _ uuid.UUID) (db.BudgetProfile, error) {
				return db.BudgetProfile{ID: profileID, UserID: uuid.New()}, nil // caller is not owner
			},
			getPersonByUserID: func(_ context.Context, _, _ uuid.UUID) (db.BudgetToProfileMapping, error) {
				return db.BudgetToProfileMapping{Role: "collaborator"}, nil
			},
		},
		&mockExpenseAllocationRepo{},
		&mockFixedExpenseRepo{},
		&mockTransactionReviewRepo{},
	)

	txs, err := svc.List(context.Background(), db.ListTransactionsParams{BudgetPeriodID: periodID}, userID)
	require.NoError(t, err)
	assert.Len(t, txs, 1)
}

func TestListTransactions_ViewerAllowed(t *testing.T) {
	userID := uuid.New()
	profileID := uuid.New()
	periodID := uuid.New()

	svc := NewTransactionService(
		&mockTransactionRepo{
			list: func(_ context.Context, _ db.ListTransactionsParams) ([]db.Transaction, error) {
				return []db.Transaction{}, nil
			},
		},
		&mockBudgetProfileRepo{
			getPeriodByID: func(_ context.Context, id uuid.UUID) (db.BudgetPeriod, error) {
				return db.BudgetPeriod{ID: id, BudgetProfileID: profileID}, nil
			},
			getByID: func(_ context.Context, _ uuid.UUID) (db.BudgetProfile, error) {
				return db.BudgetProfile{ID: profileID, UserID: uuid.New()}, nil
			},
			getPersonByUserID: func(_ context.Context, _, _ uuid.UUID) (db.BudgetToProfileMapping, error) {
				return db.BudgetToProfileMapping{Role: "viewer"}, nil
			},
		},
		&mockExpenseAllocationRepo{},
		&mockFixedExpenseRepo{},
		&mockTransactionReviewRepo{},
	)

	_, err := svc.List(context.Background(), db.ListTransactionsParams{BudgetPeriodID: periodID}, userID)
	require.NoError(t, err)
}

func TestCreateTransaction_CollaboratorAllowed(t *testing.T) {
	userID := uuid.New()
	profileID := uuid.New()
	periodID := uuid.New()

	svc := NewTransactionService(
		&mockTransactionRepo{
			create: func(_ context.Context, _ db.CreateTransactionParams) (db.Transaction, error) {
				return db.Transaction{ID: uuid.New()}, nil
			},
		},
		&mockBudgetProfileRepo{
			getPeriodByID: func(_ context.Context, id uuid.UUID) (db.BudgetPeriod, error) {
				return db.BudgetPeriod{ID: id, BudgetProfileID: profileID}, nil
			},
			getByID: func(_ context.Context, _ uuid.UUID) (db.BudgetProfile, error) {
				return db.BudgetProfile{ID: profileID, UserID: uuid.New()}, nil
			},
			getPersonByUserID: func(_ context.Context, _, _ uuid.UUID) (db.BudgetToProfileMapping, error) {
				return db.BudgetToProfileMapping{Role: "collaborator"}, nil
			},
		},
		&mockExpenseAllocationRepo{},
		&mockFixedExpenseRepo{},
		&mockTransactionReviewRepo{},
	)

	_, err := svc.Create(context.Background(), db.CreateTransactionParams{BudgetPeriodID: &periodID}, userID)
	require.NoError(t, err)
}

func TestCreateTransaction_ViewerForbidden(t *testing.T) {
	userID := uuid.New()
	profileID := uuid.New()
	periodID := uuid.New()

	svc := NewTransactionService(
		&mockTransactionRepo{},
		&mockBudgetProfileRepo{
			getPeriodByID: func(_ context.Context, id uuid.UUID) (db.BudgetPeriod, error) {
				return db.BudgetPeriod{ID: id, BudgetProfileID: profileID}, nil
			},
			getByID: func(_ context.Context, _ uuid.UUID) (db.BudgetProfile, error) {
				return db.BudgetProfile{ID: profileID, UserID: uuid.New()}, nil
			},
			getPersonByUserID: func(_ context.Context, _, _ uuid.UUID) (db.BudgetToProfileMapping, error) {
				return db.BudgetToProfileMapping{Role: "viewer"}, nil
			},
		},
		&mockExpenseAllocationRepo{},
		&mockFixedExpenseRepo{},
		&mockTransactionReviewRepo{},
	)

	_, err := svc.Create(context.Background(), db.CreateTransactionParams{BudgetPeriodID: &periodID}, userID)
	require.Error(t, err)
	var forbidden *apperr.ForbiddenError
	require.ErrorAs(t, err, &forbidden)
}

func TestCreateTransaction_Forbidden_WhenPeriodArchived(t *testing.T) {
	userID := uuid.New()
	profileID := uuid.New()
	periodID := uuid.New()

	svc := NewTransactionService(
		&mockTransactionRepo{},
		&mockBudgetProfileRepo{
			getPeriodByID: func(_ context.Context, id uuid.UUID) (db.BudgetPeriod, error) {
				return db.BudgetPeriod{ID: id, BudgetProfileID: profileID, IsArchived: true}, nil
			},
			getByID: func(_ context.Context, _ uuid.UUID) (db.BudgetProfile, error) {
				return db.BudgetProfile{ID: profileID, UserID: userID}, nil
			},
		},
		&mockExpenseAllocationRepo{},
		&mockFixedExpenseRepo{},
		&mockTransactionReviewRepo{},
	)

	_, err := svc.Create(context.Background(), db.CreateTransactionParams{BudgetPeriodID: &periodID}, userID)
	require.Error(t, err)
	var forbidden *apperr.ForbiddenError
	require.ErrorAs(t, err, &forbidden)
}

func TestCreateTransaction_Invalid_WhenDateBackdatedBeforePeriodStart(t *testing.T) {
	userID := uuid.New()
	profileID := uuid.New()
	periodID := uuid.New()
	periodStart := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	backdated := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	variableType := int32(2)

	svc := NewTransactionService(
		&mockTransactionRepo{
			create: func(_ context.Context, _ db.CreateTransactionParams) (db.Transaction, error) {
				t.Fatal("transactions.Create should not be called when the date is rejected")
				return db.Transaction{}, nil
			},
		},
		&mockBudgetProfileRepo{
			getPeriodByID: func(_ context.Context, id uuid.UUID) (db.BudgetPeriod, error) {
				return db.BudgetPeriod{ID: id, BudgetProfileID: profileID, StartDate: pgtype.Date{Time: periodStart, Valid: true}}, nil
			},
			getByID: func(_ context.Context, _ uuid.UUID) (db.BudgetProfile, error) {
				return db.BudgetProfile{ID: profileID, UserID: userID}, nil
			},
		},
		&mockExpenseAllocationRepo{},
		&mockFixedExpenseRepo{},
		&mockTransactionReviewRepo{},
	)

	_, err := svc.Create(context.Background(), db.CreateTransactionParams{
		BudgetPeriodID:    &periodID,
		TransactionTypeID: &variableType,
		Date:              pgtype.Date{Time: backdated, Valid: true},
	}, userID)
	require.Error(t, err)
	var invalid *apperr.ValidationError
	require.ErrorAs(t, err, &invalid)
}

func TestCreateTransaction_FixedTypeExemptFromBackdatingCheck(t *testing.T) {
	userID := uuid.New()
	profileID := uuid.New()
	periodID := uuid.New()
	periodStart := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	backdated := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	fixedType := int32(1)

	svc := NewTransactionService(
		&mockTransactionRepo{
			create: func(_ context.Context, _ db.CreateTransactionParams) (db.Transaction, error) {
				return db.Transaction{ID: uuid.New()}, nil
			},
		},
		&mockBudgetProfileRepo{
			getPeriodByID: func(_ context.Context, id uuid.UUID) (db.BudgetPeriod, error) {
				return db.BudgetPeriod{ID: id, BudgetProfileID: profileID, StartDate: pgtype.Date{Time: periodStart, Valid: true}}, nil
			},
			getByID: func(_ context.Context, _ uuid.UUID) (db.BudgetProfile, error) {
				return db.BudgetProfile{ID: profileID, UserID: userID}, nil
			},
		},
		&mockExpenseAllocationRepo{},
		&mockFixedExpenseRepo{},
		&mockTransactionReviewRepo{},
	)

	_, err := svc.Create(context.Background(), db.CreateTransactionParams{
		BudgetPeriodID:    &periodID,
		TransactionTypeID: &fixedType,
		Date:              pgtype.Date{Time: backdated, Valid: true},
	}, userID)
	require.NoError(t, err)
}

func TestUpdateTransaction_Invalid_WhenDateBackdatedBeforePeriodStart(t *testing.T) {
	userID := uuid.New()
	profileID := uuid.New()
	periodID := uuid.New()
	txID := uuid.New()
	periodStart := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	backdated := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	variableType := int32(2)

	svc := NewTransactionService(
		&mockTransactionRepo{
			getByID: func(_ context.Context, id uuid.UUID) (db.Transaction, error) {
				return db.Transaction{ID: id, BudgetPeriodID: &periodID}, nil
			},
			update: func(_ context.Context, _ db.UpdateTransactionParams) (db.Transaction, error) {
				t.Fatal("transactions.Update should not be called when the date is rejected")
				return db.Transaction{}, nil
			},
		},
		&mockBudgetProfileRepo{
			getPeriodByID: func(_ context.Context, id uuid.UUID) (db.BudgetPeriod, error) {
				return db.BudgetPeriod{ID: id, BudgetProfileID: profileID, StartDate: pgtype.Date{Time: periodStart, Valid: true}}, nil
			},
			getByID: func(_ context.Context, _ uuid.UUID) (db.BudgetProfile, error) {
				return db.BudgetProfile{ID: profileID, UserID: userID}, nil
			},
		},
		&mockExpenseAllocationRepo{},
		&mockFixedExpenseRepo{},
		&mockTransactionReviewRepo{},
	)

	_, err := svc.Update(context.Background(), db.UpdateTransactionParams{
		ID:                txID,
		TransactionTypeID: &variableType,
		Date:              pgtype.Date{Time: backdated, Valid: true},
	}, userID)
	require.Error(t, err)
	var invalid *apperr.ValidationError
	require.ErrorAs(t, err, &invalid)
}

// ── UpdateTransaction category-only restriction tests ─────────────────────────
// (Plaid-linked transactions, and any transaction whose period has archived,
// keep their financial record frozen -- only their category may still change.)

func baseUpdateTestTransaction(id, periodID, paymentMethodID uuid.UUID) db.Transaction {
	name := "Groceries"
	categoryID := int32(1)
	freqID := int32(1)
	typeID := int32(2)
	amount := pgtype.Numeric{}
	_ = amount.Scan("50.00")
	return db.Transaction{
		ID:                     id,
		Name:                   &name,
		Amount:                 amount,
		PlannedAmount:          amount,
		Date:                   pgtype.Date{Time: time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC), Valid: true},
		BudgetPeriodID:         &periodID,
		CategoryID:             &categoryID,
		PaymentMethodID:        &paymentMethodID,
		TransactionFrequencyID: &freqID,
		TransactionTypeID:      &typeID,
	}
}

func matchingUpdateParams(tx db.Transaction) db.UpdateTransactionParams {
	return db.UpdateTransactionParams{
		ID:                     tx.ID,
		Name:                   tx.Name,
		Amount:                 tx.Amount,
		PlannedAmount:          tx.PlannedAmount,
		Date:                   tx.Date,
		CategoryID:             tx.CategoryID,
		PaymentMethodID:        tx.PaymentMethodID,
		TransactionFrequencyID: tx.TransactionFrequencyID,
		TransactionTypeID:      tx.TransactionTypeID,
	}
}

func TestUpdateTransaction_Success_FullEdit_WhenActiveAndNotPlaidLinked(t *testing.T) {
	userID := uuid.New()
	profileID := uuid.New()
	periodID := uuid.New()
	txID := uuid.New()
	pmID := uuid.New()
	newPmID := uuid.New()
	tx := baseUpdateTestTransaction(txID, periodID, pmID)
	params := matchingUpdateParams(tx)
	// Change every field -- a fully active, non-Plaid transaction should
	// allow this.
	newAmount := pgtype.Numeric{}
	_ = newAmount.Scan("75.00")
	params.Amount = newAmount
	params.PlannedAmount = newAmount
	params.PaymentMethodID = &newPmID

	svc := NewTransactionService(
		&mockTransactionRepo{
			getByID: func(_ context.Context, _ uuid.UUID) (db.Transaction, error) { return tx, nil },
			update: func(_ context.Context, arg db.UpdateTransactionParams) (db.Transaction, error) {
				return db.Transaction{ID: arg.ID}, nil
			},
		},
		&mockBudgetProfileRepo{
			getPeriodByID: func(_ context.Context, id uuid.UUID) (db.BudgetPeriod, error) {
				return db.BudgetPeriod{ID: id, BudgetProfileID: profileID, IsArchived: false, StartDate: pgtype.Date{Time: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), Valid: true}}, nil
			},
			getByID: func(_ context.Context, _ uuid.UUID) (db.BudgetProfile, error) {
				return db.BudgetProfile{ID: profileID, UserID: userID}, nil
			},
		},
		&mockExpenseAllocationRepo{},
		&mockFixedExpenseRepo{},
		&mockTransactionReviewRepo{},
	)

	_, err := svc.Update(context.Background(), params, userID)
	require.NoError(t, err)
}

func TestUpdateTransaction_CategoryOnly_Succeeds_WhenPeriodArchived(t *testing.T) {
	userID := uuid.New()
	profileID := uuid.New()
	periodID := uuid.New()
	txID := uuid.New()
	pmID := uuid.New()
	newCategoryID := int32(2)
	tx := baseUpdateTestTransaction(txID, periodID, pmID)
	params := matchingUpdateParams(tx)
	params.CategoryID = &newCategoryID

	svc := NewTransactionService(
		&mockTransactionRepo{
			getByID: func(_ context.Context, _ uuid.UUID) (db.Transaction, error) { return tx, nil },
			update: func(_ context.Context, arg db.UpdateTransactionParams) (db.Transaction, error) {
				return db.Transaction{ID: arg.ID, CategoryID: arg.CategoryID}, nil
			},
		},
		&mockBudgetProfileRepo{
			getPeriodByID: func(_ context.Context, id uuid.UUID) (db.BudgetPeriod, error) {
				return db.BudgetPeriod{ID: id, BudgetProfileID: profileID, IsArchived: true}, nil
			},
			getByID: func(_ context.Context, _ uuid.UUID) (db.BudgetProfile, error) {
				return db.BudgetProfile{ID: profileID, UserID: userID}, nil
			},
		},
		&mockExpenseAllocationRepo{},
		&mockFixedExpenseRepo{},
		&mockTransactionReviewRepo{},
	)

	result, err := svc.Update(context.Background(), params, userID)
	require.NoError(t, err)
	assert.Equal(t, &newCategoryID, result.CategoryID)
}

func TestUpdateTransaction_Invalid_WhenAmountChangedOnArchivedPeriod(t *testing.T) {
	userID := uuid.New()
	profileID := uuid.New()
	periodID := uuid.New()
	txID := uuid.New()
	pmID := uuid.New()
	tx := baseUpdateTestTransaction(txID, periodID, pmID)
	params := matchingUpdateParams(tx)
	newAmount := pgtype.Numeric{}
	_ = newAmount.Scan("999.00")
	params.Amount = newAmount

	svc := NewTransactionService(
		&mockTransactionRepo{
			getByID: func(_ context.Context, _ uuid.UUID) (db.Transaction, error) { return tx, nil },
			update: func(_ context.Context, _ db.UpdateTransactionParams) (db.Transaction, error) {
				t.Fatal("transactions.Update should not be called when a non-category field changes on an archived period")
				return db.Transaction{}, nil
			},
		},
		&mockBudgetProfileRepo{
			getPeriodByID: func(_ context.Context, id uuid.UUID) (db.BudgetPeriod, error) {
				return db.BudgetPeriod{ID: id, BudgetProfileID: profileID, IsArchived: true}, nil
			},
			getByID: func(_ context.Context, _ uuid.UUID) (db.BudgetProfile, error) {
				return db.BudgetProfile{ID: profileID, UserID: userID}, nil
			},
		},
		&mockExpenseAllocationRepo{},
		&mockFixedExpenseRepo{},
		&mockTransactionReviewRepo{},
	)

	_, err := svc.Update(context.Background(), params, userID)
	require.Error(t, err)
	var invalid *apperr.ValidationError
	require.ErrorAs(t, err, &invalid)
}

func TestUpdateTransaction_CategoryOnly_Succeeds_ForPlaidLinkedTransaction(t *testing.T) {
	userID := uuid.New()
	profileID := uuid.New()
	periodID := uuid.New()
	txID := uuid.New()
	pmID := uuid.New()
	plaidID := "plaid-tx-123"
	newCategoryID := int32(2)
	tx := baseUpdateTestTransaction(txID, periodID, pmID)
	tx.PlaidTransactionID = &plaidID
	params := matchingUpdateParams(tx)
	params.CategoryID = &newCategoryID

	svc := NewTransactionService(
		&mockTransactionRepo{
			getByID: func(_ context.Context, _ uuid.UUID) (db.Transaction, error) { return tx, nil },
			update: func(_ context.Context, arg db.UpdateTransactionParams) (db.Transaction, error) {
				return db.Transaction{ID: arg.ID, CategoryID: arg.CategoryID}, nil
			},
		},
		&mockBudgetProfileRepo{
			// Active period -- the Plaid-linked restriction applies
			// regardless of archive state.
			getPeriodByID: func(_ context.Context, id uuid.UUID) (db.BudgetPeriod, error) {
				return db.BudgetPeriod{ID: id, BudgetProfileID: profileID, IsArchived: false, StartDate: pgtype.Date{Time: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), Valid: true}}, nil
			},
			getByID: func(_ context.Context, _ uuid.UUID) (db.BudgetProfile, error) {
				return db.BudgetProfile{ID: profileID, UserID: userID}, nil
			},
		},
		&mockExpenseAllocationRepo{},
		&mockFixedExpenseRepo{},
		&mockTransactionReviewRepo{},
	)

	result, err := svc.Update(context.Background(), params, userID)
	require.NoError(t, err)
	assert.Equal(t, &newCategoryID, result.CategoryID)
}

func TestUpdateTransaction_Invalid_WhenPaymentMethodChangedOnPlaidLinkedTransaction(t *testing.T) {
	userID := uuid.New()
	profileID := uuid.New()
	periodID := uuid.New()
	txID := uuid.New()
	pmID := uuid.New()
	newPmID := uuid.New()
	plaidID := "plaid-tx-123"
	tx := baseUpdateTestTransaction(txID, periodID, pmID)
	tx.PlaidTransactionID = &plaidID
	params := matchingUpdateParams(tx)
	params.PaymentMethodID = &newPmID

	svc := NewTransactionService(
		&mockTransactionRepo{
			getByID: func(_ context.Context, _ uuid.UUID) (db.Transaction, error) { return tx, nil },
			update: func(_ context.Context, _ db.UpdateTransactionParams) (db.Transaction, error) {
				t.Fatal("transactions.Update should not be called when a non-category field changes on a Plaid-linked transaction")
				return db.Transaction{}, nil
			},
		},
		&mockBudgetProfileRepo{
			getPeriodByID: func(_ context.Context, id uuid.UUID) (db.BudgetPeriod, error) {
				return db.BudgetPeriod{ID: id, BudgetProfileID: profileID, IsArchived: false, StartDate: pgtype.Date{Time: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), Valid: true}}, nil
			},
			getByID: func(_ context.Context, _ uuid.UUID) (db.BudgetProfile, error) {
				return db.BudgetProfile{ID: profileID, UserID: userID}, nil
			},
		},
		&mockExpenseAllocationRepo{},
		&mockFixedExpenseRepo{},
		&mockTransactionReviewRepo{},
	)

	_, err := svc.Update(context.Background(), params, userID)
	require.Error(t, err)
	var invalid *apperr.ValidationError
	require.ErrorAs(t, err, &invalid)
}

// ── CreateTransaction review-queueing tests ───────────────────────────────────

func TestCreateTransaction_QueuesReview_WhenScoreOver80(t *testing.T) {
	userID := uuid.New()
	profileID := uuid.New()
	periodID := uuid.New()
	feID := uuid.New()
	unpaidTxID := uuid.New()
	variableType := int32(2)
	txName := "Netflix"
	catID := int32(3)
	pmID := uuid.New()

	var upsertCalled bool

	feAmount := pgtype.Numeric{}
	_ = feAmount.Scan("15.99")
	txAmount := pgtype.Numeric{}
	_ = txAmount.Scan("15.99")

	svc := NewTransactionService(
		&mockTransactionRepo{
			create: func(_ context.Context, _ db.CreateTransactionParams) (db.Transaction, error) {
				return db.Transaction{
					ID:              uuid.New(),
					Name:            &txName,
					Amount:          txAmount,
					CategoryID:      &catID,
					PaymentMethodID: &pmID,
					BudgetPeriodID:  &periodID,
				}, nil
			},
		},
		&mockBudgetProfileRepo{
			getPeriodByID: func(_ context.Context, id uuid.UUID) (db.BudgetPeriod, error) {
				return db.BudgetPeriod{ID: id, BudgetProfileID: profileID}, nil
			},
			getByID: func(_ context.Context, _ uuid.UUID) (db.BudgetProfile, error) {
				return db.BudgetProfile{ID: profileID, UserID: userID}, nil
			},
		},
		&mockExpenseAllocationRepo{},
		&mockFixedExpenseRepo{
			list: func(_ context.Context, _ uuid.UUID) ([]db.FixedExpense, error) {
				fe := db.FixedExpense{
					ID:              feID,
					Name:            "Netflix",
					PlannedAmount:   feAmount,
					CategoryID:      &catID,
					PaymentMethodID: &pmID,
				}
				return []db.FixedExpense{fe}, nil
			},
			getUnpaidTransactionInPer: func(_ context.Context, arg db.GetUnpaidTransactionByFixedExpenseInPeriodParams) (db.Transaction, error) {
				// The lookup must be scoped to the transaction's own period.
				if arg.BudgetPeriodID != periodID {
					return db.Transaction{}, apperr.NotFound("transaction", arg.FixedExpenseID.String())
				}
				return db.Transaction{ID: unpaidTxID, BudgetPeriodID: &periodID}, nil
			},
		},
		&mockTransactionReviewRepo{
			upsert: func(_ context.Context, pID, tID, mID uuid.UUID, score float64) (db.TransactionReview, error) {
				assert.Equal(t, periodID, pID)
				assert.Equal(t, unpaidTxID, mID)
				assert.GreaterOrEqual(t, score, 80.0)
				upsertCalled = true
				return db.TransactionReview{}, nil
			},
		},
	)

	_, err := svc.Create(context.Background(), db.CreateTransactionParams{
		BudgetPeriodID:    &periodID,
		TransactionTypeID: &variableType,
	}, userID)
	require.NoError(t, err)
	assert.True(t, upsertCalled, "expected Upsert to be called for high-scoring match")
}

func TestCreateTransaction_NoReview_WhenScoreUnder80(t *testing.T) {
	userID := uuid.New()
	profileID := uuid.New()
	periodID := uuid.New()
	variableType := int32(2)
	txName := "Coffee Shop"

	var upsertCalled bool

	feAmount := pgtype.Numeric{}
	_ = feAmount.Scan("200.00")
	txAmount := pgtype.Numeric{}
	_ = txAmount.Scan("5.00")

	svc := NewTransactionService(
		&mockTransactionRepo{
			create: func(_ context.Context, _ db.CreateTransactionParams) (db.Transaction, error) {
				return db.Transaction{
					ID:             uuid.New(),
					Name:           &txName,
					Amount:         txAmount,
					BudgetPeriodID: &periodID,
				}, nil
			},
		},
		&mockBudgetProfileRepo{
			getPeriodByID: func(_ context.Context, id uuid.UUID) (db.BudgetPeriod, error) {
				return db.BudgetPeriod{ID: id, BudgetProfileID: profileID}, nil
			},
			getByID: func(_ context.Context, _ uuid.UUID) (db.BudgetProfile, error) {
				return db.BudgetProfile{ID: profileID, UserID: userID}, nil
			},
		},
		&mockExpenseAllocationRepo{},
		&mockFixedExpenseRepo{
			list: func(_ context.Context, _ uuid.UUID) ([]db.FixedExpense, error) {
				fe := db.FixedExpense{ID: uuid.New(), Name: "Rent", PlannedAmount: feAmount}
				return []db.FixedExpense{fe}, nil
			},
		},
		&mockTransactionReviewRepo{
			upsert: func(_ context.Context, _, _, _ uuid.UUID, _ float64) (db.TransactionReview, error) {
				upsertCalled = true
				return db.TransactionReview{}, nil
			},
		},
	)

	_, err := svc.Create(context.Background(), db.CreateTransactionParams{
		BudgetPeriodID:    &periodID,
		TransactionTypeID: &variableType,
	}, userID)
	require.NoError(t, err)
	assert.False(t, upsertCalled, "expected Upsert NOT to be called for low-scoring match")
}

func TestCreateTransaction_NoReview_ForFixedType(t *testing.T) {
	userID := uuid.New()
	profileID := uuid.New()
	periodID := uuid.New()
	fixedType := int32(1)

	var upsertCalled bool

	svc := NewTransactionService(
		&mockTransactionRepo{
			create: func(_ context.Context, _ db.CreateTransactionParams) (db.Transaction, error) {
				return db.Transaction{ID: uuid.New(), BudgetPeriodID: &periodID}, nil
			},
		},
		&mockBudgetProfileRepo{
			getPeriodByID: func(_ context.Context, id uuid.UUID) (db.BudgetPeriod, error) {
				return db.BudgetPeriod{ID: id, BudgetProfileID: profileID}, nil
			},
			getByID: func(_ context.Context, _ uuid.UUID) (db.BudgetProfile, error) {
				return db.BudgetProfile{ID: profileID, UserID: userID}, nil
			},
		},
		&mockExpenseAllocationRepo{},
		&mockFixedExpenseRepo{},
		&mockTransactionReviewRepo{
			upsert: func(_ context.Context, _, _, _ uuid.UUID, _ float64) (db.TransactionReview, error) {
				upsertCalled = true
				return db.TransactionReview{}, nil
			},
		},
	)

	_, err := svc.Create(context.Background(), db.CreateTransactionParams{
		BudgetPeriodID:    &periodID,
		TransactionTypeID: &fixedType,
	}, userID)
	require.NoError(t, err)
	assert.False(t, upsertCalled, "expected Upsert NOT to be called for fixed transactions")
}

// ── MarkTransactionForReview tests ────────────────────────────────────────────

func TestMarkTransactionForReview_Success(t *testing.T) {
	userID := uuid.New()
	profileID := uuid.New()
	periodID := uuid.New()
	txID := uuid.New()
	matchedTxID := uuid.New()
	variableType := int32(2)
	fixedType := int32(1)

	svc := NewTransactionService(
		&mockTransactionRepo{
			getByID: func(_ context.Context, id uuid.UUID) (db.Transaction, error) {
				if id == matchedTxID {
					return db.Transaction{ID: id, TransactionTypeID: &fixedType, BudgetPeriodID: &periodID}, nil
				}
				return db.Transaction{ID: id, TransactionTypeID: &variableType, BudgetPeriodID: &periodID}, nil
			},
		},
		&mockBudgetProfileRepo{
			getByID: func(_ context.Context, _ uuid.UUID) (db.BudgetProfile, error) {
				return db.BudgetProfile{ID: profileID, UserID: userID}, nil
			},
			getPeriodByID: func(_ context.Context, id uuid.UUID) (db.BudgetPeriod, error) {
				return db.BudgetPeriod{ID: id, BudgetProfileID: profileID}, nil
			},
		},
		&mockExpenseAllocationRepo{},
		&mockFixedExpenseRepo{},
		&mockTransactionReviewRepo{},
	)

	review, err := svc.MarkTransactionForReview(context.Background(), userID, txID, matchedTxID, profileID)
	require.NoError(t, err)
	assert.Equal(t, db.TransactionReview{}, review) // mock returns zero value
}

func TestMarkTransactionForReview_FiresReviewPendingNotification(t *testing.T) {
	userID := uuid.New()
	profileID := uuid.New()
	periodID := uuid.New()
	txID := uuid.New()
	matchedTxID := uuid.New()
	variableType := int32(2)
	fixedType := int32(1)
	txName := "Starbucks"

	var createdNotifications []db.CreateNotificationParams
	notifRepo := &mockNotifRepo{
		getBudgetSubscribers: func(_ context.Context, pid uuid.UUID, alertType string) ([]db.AlertSubscription, error) {
			assert.Equal(t, profileID, pid)
			assert.Equal(t, "review_pending", alertType)
			return []db.AlertSubscription{{ID: uuid.New(), UserID: uuid.New(), Channel: "in_app"}}, nil
		},
		create: func(_ context.Context, arg db.CreateNotificationParams) (db.Notification, error) {
			createdNotifications = append(createdNotifications, arg)
			return db.Notification{}, nil
		},
	}
	profileRepo := &mockBudgetProfileRepo{
		getByID: func(_ context.Context, _ uuid.UUID) (db.BudgetProfile, error) {
			return db.BudgetProfile{ID: profileID, UserID: userID}, nil
		},
		getPeriodByID: func(_ context.Context, id uuid.UUID) (db.BudgetPeriod, error) {
			return db.BudgetPeriod{ID: id, BudgetProfileID: profileID}, nil
		},
	}
	notifSvc := newTestNotifSvc(notifRepo, profileRepo)

	svc := NewTransactionService(
		&mockTransactionRepo{
			getByID: func(_ context.Context, id uuid.UUID) (db.Transaction, error) {
				if id == matchedTxID {
					return db.Transaction{ID: id, TransactionTypeID: &fixedType, BudgetPeriodID: &periodID}, nil
				}
				return db.Transaction{ID: id, Name: &txName, TransactionTypeID: &variableType, BudgetPeriodID: &periodID}, nil
			},
		},
		profileRepo,
		&mockExpenseAllocationRepo{},
		&mockFixedExpenseRepo{},
		&mockTransactionReviewRepo{},
	).WithNotifications(notifSvc)

	_, err := svc.MarkTransactionForReview(context.Background(), userID, txID, matchedTxID, profileID)
	require.NoError(t, err)

	require.Len(t, createdNotifications, 1)
	assert.Equal(t, "review_pending", createdNotifications[0].AlertType)
	assert.Contains(t, createdNotifications[0].Body, txName)
}

func TestMarkTransactionForReview_Forbidden_WhenViewer(t *testing.T) {
	userID := uuid.New()
	profileID := uuid.New()

	svc := NewTransactionService(
		&mockTransactionRepo{},
		&mockBudgetProfileRepo{
			getByID: func(_ context.Context, _ uuid.UUID) (db.BudgetProfile, error) {
				return db.BudgetProfile{ID: profileID, UserID: uuid.New()}, nil
			},
			getPersonByUserID: func(_ context.Context, _, _ uuid.UUID) (db.BudgetToProfileMapping, error) {
				return db.BudgetToProfileMapping{Role: "viewer"}, nil
			},
		},
		&mockExpenseAllocationRepo{},
		&mockFixedExpenseRepo{},
		&mockTransactionReviewRepo{},
	)

	_, err := svc.MarkTransactionForReview(context.Background(), userID, uuid.New(), uuid.New(), profileID)
	require.Error(t, err)
	var forbidden *apperr.ForbiddenError
	require.ErrorAs(t, err, &forbidden)
}

func TestMarkTransactionForReview_Invalid_WhenFixed(t *testing.T) {
	userID := uuid.New()
	profileID := uuid.New()
	periodID := uuid.New()
	typeID := int32(1) // Fixed

	svc := NewTransactionService(
		&mockTransactionRepo{
			getByID: func(_ context.Context, id uuid.UUID) (db.Transaction, error) {
				return db.Transaction{ID: id, TransactionTypeID: &typeID, BudgetPeriodID: &periodID}, nil
			},
		},
		&mockBudgetProfileRepo{
			getByID: func(_ context.Context, _ uuid.UUID) (db.BudgetProfile, error) {
				return db.BudgetProfile{ID: profileID, UserID: userID}, nil
			},
		},
		&mockExpenseAllocationRepo{},
		&mockFixedExpenseRepo{},
		&mockTransactionReviewRepo{},
	)

	_, err := svc.MarkTransactionForReview(context.Background(), userID, uuid.New(), uuid.New(), profileID)
	require.Error(t, err)
	var invalid *apperr.ValidationError
	require.ErrorAs(t, err, &invalid)
}

func TestMarkTransactionForReview_Forbidden_WhenMatchedTransactionOtherPeriod(t *testing.T) {
	userID := uuid.New()
	profileID := uuid.New()
	periodID := uuid.New()
	otherPeriodID := uuid.New()
	matchedTxID := uuid.New()
	variableType := int32(2)
	fixedType := int32(1)

	svc := NewTransactionService(
		&mockTransactionRepo{
			getByID: func(_ context.Context, id uuid.UUID) (db.Transaction, error) {
				if id == matchedTxID {
					return db.Transaction{ID: id, TransactionTypeID: &fixedType, BudgetPeriodID: &otherPeriodID}, nil
				}
				return db.Transaction{ID: id, TransactionTypeID: &variableType, BudgetPeriodID: &periodID}, nil
			},
		},
		&mockBudgetProfileRepo{
			getByID: func(_ context.Context, _ uuid.UUID) (db.BudgetProfile, error) {
				return db.BudgetProfile{ID: profileID, UserID: userID}, nil
			},
			getPeriodByID: func(_ context.Context, id uuid.UUID) (db.BudgetPeriod, error) {
				return db.BudgetPeriod{ID: id, BudgetProfileID: profileID}, nil
			},
		},
		&mockExpenseAllocationRepo{},
		&mockFixedExpenseRepo{},
		&mockTransactionReviewRepo{},
	)

	_, err := svc.MarkTransactionForReview(context.Background(), userID, uuid.New(), matchedTxID, profileID)
	require.Error(t, err)
	var forbidden *apperr.ForbiddenError
	require.ErrorAs(t, err, &forbidden)
}

// ── ConfirmTransactionReview tests ────────────────────────────────────────────

func TestConfirmTransactionReview_FixedExpenseMatch_MarksPaidAndSavesAlias(t *testing.T) {
	userID := uuid.New()
	profileID := uuid.New()
	periodID := uuid.New()
	reviewID := uuid.New()
	importedTxID := uuid.New()
	matchedTxID := uuid.New()
	feID := uuid.New()
	importedName := "NETFLIX.COM"

	importedAmount := pgtype.Numeric{}
	_ = importedAmount.Scan("800.00")

	var markedPaidID uuid.UUID
	var markedPaidAmount pgtype.Numeric
	var aliasFEID uuid.UUID
	var aliasText string
	var confirmedStatus string
	var excludedID uuid.UUID
	var excludedFlag bool

	svc := NewTransactionService(
		&mockTransactionRepo{
			getByID: func(_ context.Context, id uuid.UUID) (db.Transaction, error) {
				if id == matchedTxID {
					return db.Transaction{ID: matchedTxID, IsPaid: false, BudgetPeriodID: &periodID, FixedExpenseID: &feID}, nil
				}
				return db.Transaction{ID: importedTxID, Name: &importedName, Amount: importedAmount}, nil
			},
			markAsPaid: func(_ context.Context, arg db.MarkTransactionAsPaidParams) (db.Transaction, error) {
				markedPaidID = arg.ID
				markedPaidAmount = arg.Amount
				return db.Transaction{ID: arg.ID}, nil
			},
			setExcluded: func(_ context.Context, arg db.SetTransactionExcludedParams) (db.Transaction, error) {
				excludedID = arg.ID
				excludedFlag = arg.Excluded
				return db.Transaction{ID: arg.ID, IsExcluded: arg.Excluded}, nil
			},
		},
		&mockBudgetProfileRepo{
			getPeriodByID: func(_ context.Context, id uuid.UUID) (db.BudgetPeriod, error) {
				return db.BudgetPeriod{ID: id, BudgetProfileID: profileID}, nil
			},
			getByID: func(_ context.Context, _ uuid.UUID) (db.BudgetProfile, error) {
				return db.BudgetProfile{ID: profileID, UserID: userID}, nil
			},
		},
		&mockExpenseAllocationRepo{},
		&mockFixedExpenseRepo{},
		&mockTransactionReviewRepo{
			getByID: func(_ context.Context, id uuid.UUID) (db.TransactionReview, error) {
				return db.TransactionReview{ID: id, BudgetPeriodID: periodID, TransactionID: importedTxID, MatchedTransactionID: matchedTxID}, nil
			},
			createAlias: func(_ context.Context, fixedExpenseID uuid.UUID, alias string) error {
				aliasFEID = fixedExpenseID
				aliasText = alias
				return nil
			},
			updateStatus: func(_ context.Context, _ uuid.UUID, status string) error {
				confirmedStatus = status
				return nil
			},
		},
	)

	err := svc.ConfirmTransactionReview(context.Background(), userID, reviewID, profileID)
	require.NoError(t, err)
	assert.Equal(t, matchedTxID, markedPaidID, "should mark the matched transaction paid, not the imported one")
	paidF, _ := markedPaidAmount.Float64Value()
	importedF, _ := importedAmount.Float64Value()
	assert.Equal(t, importedF.Float64, paidF.Float64, "should use the imported tx's actual amount, not the fixed expense planned amount")
	assert.Equal(t, feID, aliasFEID)
	assert.Equal(t, importedName, aliasText)
	assert.Equal(t, "confirmed", confirmedStatus)
	assert.Equal(t, importedTxID, excludedID, "should exclude the imported transaction from totals, not the matched one")
	assert.True(t, excludedFlag, "the imported transaction stays visible but excluded, same as Income — not hidden from ListTransactions")
}

// Confirming a match should record the real-world paid date (when the
// imported transaction actually cleared) and sync the template's
// category/payment method to what was actually observed — not echo back the
// fixed transaction's own stale scheduled date/category, which is what
// day/category propagation would otherwise silently no-op against.
func TestConfirmTransactionReview_UsesImportedDateAndSyncsObservedCategoryAndPaymentMethod(t *testing.T) {
	userID := uuid.New()
	profileID := uuid.New()
	periodID := uuid.New()
	reviewID := uuid.New()
	importedTxID := uuid.New()
	matchedTxID := uuid.New()
	feID := uuid.New()
	importedName := "NETFLIX.COM"
	importedDate := pgtype.Date{Time: time.Date(2026, time.September, 12, 0, 0, 0, 0, time.UTC), Valid: true}
	scheduledDate := pgtype.Date{Time: time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC), Valid: true}
	observedCategoryID := int32(7)
	observedPMID := uuid.New()

	importedAmount := numericFromString(t, "8.00")

	var markedPaidDate pgtype.Date
	var updated *db.UpdateFixedExpenseFromPaymentParams

	svc := NewTransactionService(
		&mockTransactionRepo{
			getByID: func(_ context.Context, id uuid.UUID) (db.Transaction, error) {
				if id == matchedTxID {
					return db.Transaction{ID: matchedTxID, IsPaid: false, BudgetPeriodID: &periodID, FixedExpenseID: &feID, Date: scheduledDate}, nil
				}
				return db.Transaction{
					ID: importedTxID, Name: &importedName, Amount: importedAmount, Date: importedDate,
					CategoryID: &observedCategoryID, PaymentMethodID: &observedPMID,
				}, nil
			},
			markAsPaid: func(_ context.Context, arg db.MarkTransactionAsPaidParams) (db.Transaction, error) {
				markedPaidDate = arg.PaidDate
				return db.Transaction{ID: arg.ID, FixedExpenseID: &feID}, nil
			},
			setExcluded: func(_ context.Context, arg db.SetTransactionExcludedParams) (db.Transaction, error) {
				return db.Transaction{ID: arg.ID, IsExcluded: arg.Excluded}, nil
			},
		},
		&mockBudgetProfileRepo{
			getPeriodByID: func(_ context.Context, id uuid.UUID) (db.BudgetPeriod, error) {
				return db.BudgetPeriod{ID: id, BudgetProfileID: profileID}, nil
			},
			getByID: func(_ context.Context, _ uuid.UUID) (db.BudgetProfile, error) {
				return db.BudgetProfile{ID: profileID, UserID: userID, AutoUpdatePlannedAmount: true}, nil
			},
		},
		&mockExpenseAllocationRepo{},
		&mockFixedExpenseRepo{
			getByID: func(_ context.Context, id uuid.UUID) (db.FixedExpense, error) {
				return db.FixedExpense{ID: id}, nil
			},
			updateFromPayment: func(_ context.Context, arg db.UpdateFixedExpenseFromPaymentParams) error {
				updated = &arg
				return nil
			},
		},
		&mockTransactionReviewRepo{
			getByID: func(_ context.Context, id uuid.UUID) (db.TransactionReview, error) {
				return db.TransactionReview{ID: id, BudgetPeriodID: periodID, TransactionID: importedTxID, MatchedTransactionID: matchedTxID}, nil
			},
			createAlias: func(_ context.Context, _ uuid.UUID, _ string) error { return nil },
			updateStatus: func(_ context.Context, _ uuid.UUID, _ string) error { return nil },
		},
	)

	err := svc.ConfirmTransactionReview(context.Background(), userID, reviewID, profileID)
	require.NoError(t, err)
	assert.Equal(t, importedDate, markedPaidDate, "should record when the bill actually cleared, not its original scheduled date")
	require.NotNil(t, updated, "the template should sync when the budget opted in")
	assert.Equal(t, int32(12), updated.DayOfMonth)
	require.NotNil(t, updated.CategoryID)
	assert.Equal(t, observedCategoryID, *updated.CategoryID)
	require.NotNil(t, updated.PaymentMethodID)
	assert.Equal(t, observedPMID, *updated.PaymentMethodID)
}

func TestConfirmTransactionReview_SavingsMatch_MarksPaidWithoutAlias(t *testing.T) {
	userID := uuid.New()
	profileID := uuid.New()
	periodID := uuid.New()
	reviewID := uuid.New()
	importedTxID := uuid.New()
	matchedTxID := uuid.New() // savings-derived: no FixedExpenseID

	var markedPaidID uuid.UUID
	aliasCalled := false

	svc := NewTransactionService(
		&mockTransactionRepo{
			getByID: func(_ context.Context, id uuid.UUID) (db.Transaction, error) {
				if id == matchedTxID {
					return db.Transaction{ID: matchedTxID, IsPaid: false, BudgetPeriodID: &periodID}, nil
				}
				return db.Transaction{ID: importedTxID}, nil
			},
			markAsPaid: func(_ context.Context, arg db.MarkTransactionAsPaidParams) (db.Transaction, error) {
				markedPaidID = arg.ID
				return db.Transaction{ID: arg.ID}, nil
			},
		},
		&mockBudgetProfileRepo{
			getPeriodByID: func(_ context.Context, id uuid.UUID) (db.BudgetPeriod, error) {
				return db.BudgetPeriod{ID: id, BudgetProfileID: profileID}, nil
			},
			getByID: func(_ context.Context, _ uuid.UUID) (db.BudgetProfile, error) {
				return db.BudgetProfile{ID: profileID, UserID: userID}, nil
			},
		},
		&mockExpenseAllocationRepo{},
		&mockFixedExpenseRepo{},
		&mockTransactionReviewRepo{
			getByID: func(_ context.Context, id uuid.UUID) (db.TransactionReview, error) {
				return db.TransactionReview{ID: id, BudgetPeriodID: periodID, TransactionID: importedTxID, MatchedTransactionID: matchedTxID}, nil
			},
			createAlias: func(_ context.Context, _ uuid.UUID, _ string) error {
				aliasCalled = true
				return nil
			},
		},
	)

	err := svc.ConfirmTransactionReview(context.Background(), userID, reviewID, profileID)
	require.NoError(t, err)
	assert.Equal(t, matchedTxID, markedPaidID)
	assert.False(t, aliasCalled, "savings-derived matches have no FixedExpense template to alias against")
}

func TestConfirmTransactionReview_AlreadyPaid_SkipsMarkAsPaid(t *testing.T) {
	userID := uuid.New()
	profileID := uuid.New()
	periodID := uuid.New()
	reviewID := uuid.New()
	matchedTxID := uuid.New()

	markAsPaidCalled := false

	svc := NewTransactionService(
		&mockTransactionRepo{
			getByID: func(_ context.Context, id uuid.UUID) (db.Transaction, error) {
				if id == matchedTxID {
					return db.Transaction{ID: matchedTxID, IsPaid: true, BudgetPeriodID: &periodID}, nil
				}
				return db.Transaction{ID: id}, nil
			},
			markAsPaid: func(_ context.Context, arg db.MarkTransactionAsPaidParams) (db.Transaction, error) {
				markAsPaidCalled = true
				return db.Transaction{ID: arg.ID}, nil
			},
		},
		&mockBudgetProfileRepo{
			getPeriodByID: func(_ context.Context, id uuid.UUID) (db.BudgetPeriod, error) {
				return db.BudgetPeriod{ID: id, BudgetProfileID: profileID}, nil
			},
			getByID: func(_ context.Context, _ uuid.UUID) (db.BudgetProfile, error) {
				return db.BudgetProfile{ID: profileID, UserID: userID}, nil
			},
		},
		&mockExpenseAllocationRepo{},
		&mockFixedExpenseRepo{},
		&mockTransactionReviewRepo{
			getByID: func(_ context.Context, id uuid.UUID) (db.TransactionReview, error) {
				return db.TransactionReview{ID: id, BudgetPeriodID: periodID, MatchedTransactionID: matchedTxID}, nil
			},
		},
	)

	err := svc.ConfirmTransactionReview(context.Background(), userID, reviewID, profileID)
	require.NoError(t, err)
	assert.False(t, markAsPaidCalled, "an already-paid match target should not be re-marked")
}

func TestUnmarkTransactionAsPaid_ResetsConfirmedReview(t *testing.T) {
	userID := uuid.New()
	profileID := uuid.New()
	periodID := uuid.New()
	txID := uuid.New()
	importedTxID := uuid.New()
	feID := uuid.New()
	importedName := "NETFLIX.COM"

	var deletedAliasFEID uuid.UUID
	var deletedAliasText string
	var resetTxID uuid.UUID
	var unexcludedID uuid.UUID
	unexcludedFlag := true // sentinel so a missing call is visible as "true" in the assertion below

	svc := NewTransactionService(
		&mockTransactionRepo{
			unmarkAsPaid: func(_ context.Context, arg db.UnmarkTransactionAsPaidParams) (db.Transaction, error) {
				return db.Transaction{ID: arg.ID, IsPaid: false, FixedExpenseID: &feID}, nil
			},
			getByID: func(_ context.Context, id uuid.UUID) (db.Transaction, error) {
				return db.Transaction{ID: id, Name: &importedName}, nil
			},
			setExcluded: func(_ context.Context, arg db.SetTransactionExcludedParams) (db.Transaction, error) {
				unexcludedID = arg.ID
				unexcludedFlag = arg.Excluded
				return db.Transaction{ID: arg.ID, IsExcluded: arg.Excluded}, nil
			},
		},
		&mockBudgetProfileRepo{
			getPeriodByID: func(_ context.Context, id uuid.UUID) (db.BudgetPeriod, error) {
				return db.BudgetPeriod{ID: id, BudgetProfileID: profileID}, nil
			},
			getByID: func(_ context.Context, _ uuid.UUID) (db.BudgetProfile, error) {
				return db.BudgetProfile{ID: profileID, UserID: userID}, nil
			},
		},
		&mockExpenseAllocationRepo{},
		&mockFixedExpenseRepo{},
		&mockTransactionReviewRepo{
			getConfirmedByMatchedTx: func(_ context.Context, matchedTransactionID uuid.UUID) (db.TransactionReview, error) {
				return db.TransactionReview{TransactionID: importedTxID, MatchedTransactionID: matchedTransactionID}, nil
			},
			deleteAlias: func(_ context.Context, fixedExpenseID uuid.UUID, alias string) error {
				deletedAliasFEID = fixedExpenseID
				deletedAliasText = alias
				return nil
			},
			resetByMatchedTx: func(_ context.Context, matchedTransactionID uuid.UUID) error {
				resetTxID = matchedTransactionID
				return nil
			},
		},
	)

	_, err := svc.UnmarkTransactionAsPaid(context.Background(), txID, periodID, userID)
	require.NoError(t, err)
	assert.Equal(t, txID, resetTxID)
	assert.Equal(t, feID, deletedAliasFEID)
	assert.Equal(t, importedName, deletedAliasText)
	assert.Equal(t, importedTxID, unexcludedID, "should un-exclude the imported transaction, not the fixed one being unmarked")
	assert.False(t, unexcludedFlag, "unmarking paid should restore the imported transaction to a normal, non-excluded row")
}
