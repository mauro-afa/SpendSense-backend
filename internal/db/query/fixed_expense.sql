-- name: CreateFixedExpense :one
INSERT INTO fixed_expense (budget_profile_id, name, planned_amount, category_id, payment_method_id, day_of_month, interval_months, anchor_date, frequency_unit, interval_weeks, day_of_week, end_date, total_payments, is_installment_plan)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
RETURNING id, budget_profile_id, name, planned_amount, category_id, payment_method_id, day_of_month, is_active, created_at, interval_months, anchor_date, frequency_unit, interval_weeks, day_of_week, end_date, total_payments, is_installment_plan;

-- name: GetFixedExpense :one
SELECT id, budget_profile_id, name, planned_amount, category_id, payment_method_id, day_of_month, is_active, created_at, interval_months, anchor_date, frequency_unit, interval_weeks, day_of_week, end_date, total_payments, is_installment_plan
FROM fixed_expense
WHERE id = $1
LIMIT 1;

-- name: ListFixedExpenses :many
SELECT id, budget_profile_id, name, planned_amount, category_id, payment_method_id, day_of_month, is_active, created_at, interval_months, anchor_date, frequency_unit, interval_weeks, day_of_week, end_date, total_payments, is_installment_plan
FROM fixed_expense
WHERE budget_profile_id = $1 AND is_active = TRUE
ORDER BY name;

-- name: UpdateFixedExpense :one
UPDATE fixed_expense
SET name              = sqlc.arg('name'),
    planned_amount    = sqlc.arg('planned_amount'),
    category_id       = sqlc.arg('category_id'),
    payment_method_id = sqlc.arg('payment_method_id'),
    day_of_month      = sqlc.arg('day_of_month'),
    interval_months   = sqlc.arg('interval_months'),
    anchor_date       = sqlc.arg('anchor_date'),
    frequency_unit    = sqlc.arg('frequency_unit'),
    interval_weeks    = sqlc.arg('interval_weeks'),
    day_of_week       = sqlc.arg('day_of_week'),
    end_date          = sqlc.arg('end_date'),
    total_payments    = sqlc.arg('total_payments')
WHERE id = sqlc.arg('id')::uuid
  AND budget_profile_id = sqlc.arg('budget_profile_id')::uuid
RETURNING id, budget_profile_id, name, planned_amount, category_id, payment_method_id, day_of_month, is_active, created_at, interval_months, anchor_date, frequency_unit, interval_weeks, day_of_week, end_date, total_payments, is_installment_plan;

-- name: FixedExpenseHasTransactionInMonth :one
SELECT EXISTS (
    SELECT 1 FROM transaction
    WHERE fixed_expense_id = sqlc.arg('fixed_expense_id')::uuid
      AND date >= sqlc.arg('month_start')::date
      AND date < sqlc.arg('month_end')::date
) AS exists;

-- name: FixedExpenseHasTransactionOnDate :one
SELECT EXISTS (
    SELECT 1 FROM transaction
    WHERE fixed_expense_id = sqlc.arg('fixed_expense_id')::uuid
      AND date = sqlc.arg('target_date')::date
) AS exists;

-- name: UpdateFixedExpenseFromPayment :exec
-- Brings the template up to what actually happened when a bill was paid:
-- amount, due date (day_of_month/day_of_week/anchor_date, all derived
-- together from the same real paid date), category, and payment method. See
-- docs/features/planned-amount-follows-paid.md for which fields the caller
-- is expected to leave unchanged when it has no better value (category_id/
-- payment_method_id keep the template's own when nothing was observed).
UPDATE fixed_expense
SET planned_amount    = sqlc.arg('planned_amount'),
    day_of_month      = sqlc.arg('day_of_month'),
    day_of_week       = sqlc.arg('day_of_week'),
    anchor_date       = sqlc.arg('anchor_date'),
    category_id       = sqlc.arg('category_id'),
    payment_method_id = sqlc.arg('payment_method_id')
WHERE id = sqlc.arg('id')::uuid;

-- name: DeactivateFixedExpense :exec
UPDATE fixed_expense
SET is_active = FALSE
WHERE id = sqlc.arg('id')::uuid
  AND budget_profile_id = sqlc.arg('budget_profile_id')::uuid;

-- name: GetUnpaidTransactionByFixedExpense :one
SELECT id, name, amount, planned_amount, date, renewal_date,
       budget_period_id, category_id, payment_method_id, transaction_frequency_id, transaction_type_id,
       is_paid, paid_date, fixed_expense_id, plaid_transaction_id, is_excluded, installment_fixed_expense_id, carried_from_budget_period_id,
       plaid_pfc_primary, plaid_pfc_detailed, plaid_reference_number, plaid_ppd_id
FROM transaction
WHERE fixed_expense_id = sqlc.arg('fixed_expense_id')::uuid
  AND is_paid = FALSE
  AND budget_period_id IN (
      SELECT id FROM budget_period
      WHERE budget_profile_id = sqlc.arg('budget_profile_id')::uuid
        AND is_archived = FALSE
  )
ORDER BY date DESC NULLS LAST
LIMIT 1;

-- Same as above but scoped to one period. Used by every auto-match path: a
-- payment may only settle the bill for the period it actually falls in.
-- GetUnpaidTransactionByFixedExpense searches every live period newest-first,
-- so a transaction imported into a closed period would reach forward and mark
-- the *next* period's bill paid (see issue #41).
-- name: GetUnpaidTransactionByFixedExpenseInPeriod :one
SELECT id, name, amount, planned_amount, date, renewal_date,
       budget_period_id, category_id, payment_method_id, transaction_frequency_id, transaction_type_id,
       is_paid, paid_date, fixed_expense_id, plaid_transaction_id, is_excluded, installment_fixed_expense_id, carried_from_budget_period_id,
       plaid_pfc_primary, plaid_pfc_detailed, plaid_reference_number, plaid_ppd_id
FROM transaction
WHERE fixed_expense_id = sqlc.arg('fixed_expense_id')::uuid
  AND is_paid = FALSE
  AND budget_period_id = sqlc.arg('budget_period_id')::uuid
ORDER BY date DESC NULLS LAST
LIMIT 1;

-- name: DeleteUnpaidTransactionByFixedExpense :exec
DELETE FROM transaction
WHERE fixed_expense_id = sqlc.arg('fixed_expense_id')::uuid
  AND is_paid = FALSE
  AND budget_period_id IN (
      SELECT id FROM budget_period
      WHERE budget_profile_id = sqlc.arg('budget_profile_id')::uuid
        AND is_archived = FALSE
  );

-- name: UpdateTransactionFromFixedExpense :exec
UPDATE transaction
SET name              = sqlc.arg('name'),
    planned_amount    = sqlc.arg('planned_amount'),
    amount            = sqlc.arg('planned_amount'),
    category_id       = sqlc.arg('category_id'),
    payment_method_id = sqlc.arg('payment_method_id'),
    date              = sqlc.arg('date')
WHERE fixed_expense_id = sqlc.arg('fixed_expense_id')::uuid
  AND is_paid = FALSE
  AND budget_period_id IN (
      SELECT id FROM budget_period
      WHERE budget_profile_id = sqlc.arg('budget_profile_id')::uuid
        AND is_archived = FALSE
  );

-- Reconciling an edited template needs to know whether the current period's
-- bill exists at all, not whether it is unpaid. GetUnpaidTransactionByFixedExpense
-- answers the narrower question, and UpdateFixedExpense used to read a miss from
-- it as "this bill just became due" and spawn a second transaction — so editing a
-- bill after marking it paid produced a duplicate (issue #62).
-- name: GetTransactionByFixedExpense :one
SELECT id, name, amount, planned_amount, date, renewal_date,
       budget_period_id, category_id, payment_method_id, transaction_frequency_id, transaction_type_id,
       is_paid, paid_date, fixed_expense_id, plaid_transaction_id, is_excluded, installment_fixed_expense_id, carried_from_budget_period_id,
       plaid_pfc_primary, plaid_pfc_detailed, plaid_reference_number, plaid_ppd_id
FROM transaction
WHERE fixed_expense_id = sqlc.arg('fixed_expense_id')::uuid
  AND budget_period_id IN (
      SELECT id FROM budget_period
      WHERE budget_profile_id = sqlc.arg('budget_profile_id')::uuid
        AND is_archived = FALSE
  )
ORDER BY is_paid, date DESC NULLS LAST
LIMIT 1;

-- Propagate a template edit onto an already-paid transaction.
--
-- Deliberately narrower than UpdateTransactionFromFixedExpense: `amount` is what
-- the user actually paid and `planned_amount` is what this period was planned at,
-- and neither is the template's business once the bill is settled. Only future
-- periods follow the template's amount — the same rule
-- docs/features/planned-amount-follows-paid.md already sets for the reverse
-- direction. is_paid/paid_date are untouched for the same reason: editing a
-- bill's category must never quietly un-pay it.
-- name: UpdatePaidTransactionFromFixedExpense :exec
UPDATE transaction
SET name              = sqlc.arg('name'),
    category_id       = sqlc.arg('category_id'),
    payment_method_id = sqlc.arg('payment_method_id')
WHERE fixed_expense_id = sqlc.arg('fixed_expense_id')::uuid
  AND is_paid = TRUE
  AND budget_period_id IN (
      SELECT id FROM budget_period
      WHERE budget_profile_id = sqlc.arg('budget_profile_id')::uuid
        AND is_archived = FALSE
  );
