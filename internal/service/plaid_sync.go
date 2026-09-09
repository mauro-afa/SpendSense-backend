package service

import (
	"context"
	"fmt"
	"log"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/BeWellSpent/wellspent-backend/internal/category"
	"github.com/BeWellSpent/wellspent-backend/internal/crypto"
	plaidclient "github.com/BeWellSpent/wellspent-backend/internal/plaid"
	db "github.com/BeWellSpent/wellspent-backend/internal/sqlc"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// AccountImport is how many transactions one connected account contributed in
// a sync run. Kept sorted by account name so a run's output — and its tests —
// are deterministic rather than depending on map iteration order.
type AccountImport struct {
	Account string
	Count   int
}

// ItemSyncResult is the outcome of syncing one connection.
type ItemSyncResult struct {
	ItemID           uuid.UUID
	InstitutionName  string
	Imported         int
	AutoConfirmed    int
	Queued           int
	SkippedNoPeriod  int
	SkippedDuplicate int
	Modified         int
	Removed          int
	// Repointed counts a pending transaction Plaid settled under a new id,
	// repointed onto the existing local row instead of being deleted and
	// reimported as a fresh, unlinked duplicate — see settlePendingTransaction.
	Repointed int
	// ByAccount counts only transactions that ended up newly available to the
	// user — auto-confirmed and queued-for-review ones are reported through
	// their own channels, so counting them here would double-notify.
	ByAccount []AccountImport
	// SkippedUnentitled is set when the connection's owner is on the free
	// plan and the sync was skipped. Deliberately a reported outcome rather
	// than a silent no-op: this state went unnoticed in production for 16
	// days because the skip returned nil and logged one line.
	SkippedUnentitled bool
	Err               error
}

// ProfileSyncResult groups the connections of one budget profile.
type ProfileSyncResult struct {
	ProfileID uuid.UUID
	Items     []ItemSyncResult
}

// SyncAll syncs every connection currently due, grouped by budget profile.
//
// Grouping is the point: entitlement, budget periods, and notifications are
// all per-profile, so processing a flat list of connections meant a budget
// with several banks sent one notification per bank and reported failures
// against a bare item UUID with no indication of whose budget it was.
func (s *PlaidService) SyncAll(ctx context.Context) ([]ProfileSyncResult, error) {
	items, err := s.items.ListActiveForSync(ctx)
	if err != nil {
		return nil, err
	}

	var profiles []ProfileSyncResult
	indexByProfile := make(map[uuid.UUID]int, len(items))
	for _, item := range items {
		res, syncErr := s.syncItemCore(ctx, item)
		res.Err = syncErr

		idx, seen := indexByProfile[item.BudgetProfileID]
		if !seen {
			profiles = append(profiles, ProfileSyncResult{ProfileID: item.BudgetProfileID})
			idx = len(profiles) - 1
			indexByProfile[item.BudgetProfileID] = idx
		}
		profiles[idx].Items = append(profiles[idx].Items, res)
	}

	// Notify once per profile, after all of its connections are done, so a
	// budget with four banks gets one summary naming each rather than four
	// separate "new transactions imported" notifications.
	for _, profile := range profiles {
		s.notifyProfile(ctx, profile)
	}
	return profiles, nil
}

// SyncProfile forces an immediate sync of one budget profile's connections,
// bypassing SyncAll's daily cooldown. Used by cycle-budgets right before a
// profile's period closes, so its totals reflect Plaid's latest data (#68).
func (s *PlaidService) SyncProfile(ctx context.Context, profileID uuid.UUID) (ProfileSyncResult, error) {
	items, err := s.items.ListActiveForProfileSync(ctx, profileID)
	if err != nil {
		return ProfileSyncResult{ProfileID: profileID}, err
	}

	result := ProfileSyncResult{ProfileID: profileID}
	for _, item := range items {
		res, syncErr := s.syncItemCore(ctx, item)
		res.Err = syncErr
		result.Items = append(result.Items, res)
	}
	s.notifyProfile(ctx, result)
	return result, nil
}

// SyncItem syncs a single connection and notifies for it directly.
//
// Kept for the immediate sync fired after a connection is created, where
// there's no run to aggregate into.
func (s *PlaidService) SyncItem(ctx context.Context, item db.PlaidItem) error {
	res, err := s.syncItemCore(ctx, item)
	res.Err = err
	s.notifyProfile(ctx, ProfileSyncResult{ProfileID: item.BudgetProfileID, Items: []ItemSyncResult{res}})
	return err
}

// notifyProfile sends the per-budget summaries for one profile's run.
func (s *PlaidService) notifyProfile(ctx context.Context, profile ProfileSyncResult) {
	if s.notifs == nil {
		return
	}
	totals := map[string]int{}
	queued := 0
	for _, item := range profile.Items {
		for _, acct := range item.ByAccount {
			totals[acct.Account] += acct.Count
		}
		queued += item.Queued
	}
	if imports := sortedAccountImports(totals); len(imports) > 0 {
		s.notifs.HandlePlaidTransactionsImported(ctx, profile.ProfileID, imports)
	}
	if queued > 0 {
		s.notifs.HandleReviewPendingBatch(ctx, profile.ProfileID, queued)
	}
}

// sortedAccountImports flattens per-account counts into a stable order:
// busiest account first, then by name so ties never reorder between runs.
func sortedAccountImports(totals map[string]int) []AccountImport {
	if len(totals) == 0 {
		return nil
	}
	out := make([]AccountImport, 0, len(totals))
	for account, count := range totals {
		out = append(out, AccountImport{Account: account, Count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Account < out[j].Account
	})
	return out
}

// syncItemCore performs an incremental Plaid transactions sync for a single
// connected item. It imports new transactions, handles modifications and
// removals, and queues or auto-confirms matches against active fixed expenses
// in the same budget. Notification is the caller's job, so a run can
// aggregate across a budget's connections.
//
// Safe to call from a goroutine; uses a separate context so the caller's
// request context doesn't cancel the background work.
func (s *PlaidService) syncItemCore(ctx context.Context, item db.PlaidItem) (ItemSyncResult, error) {
	result := ItemSyncResult{ItemID: item.ID}
	if item.InstitutionName != nil {
		result.InstitutionName = *item.InstitutionName
	}

	// Plaid sync is entitled per connection owner, not per budget — a paid
	// budget shared with a free-tier member does not cover that member's own
	// connections. Reported rather than silently skipped so it shows up in
	// the run summary and in the clients' warning banner.
	if owner, err := s.users.GetByID(ctx, item.UserID); err == nil && owner.Plan == "free" {
		log.Printf("plaid sync: skipping item %s (%s) — owner %s is on free tier", item.ID, result.InstitutionName, item.UserID)
		result.SkippedUnentitled = true
		return result, nil
	}
	categoryIDs, err := s.transactions.ListSystemCategories(ctx)
	if err != nil {
		return result, err
	}

	cursor := ""
	if item.Cursor != nil {
		cursor = *item.Cursor
	}

	accessToken, err := crypto.Decrypt(item.AccessToken, s.encryptionKey)
	if err != nil {
		return result, err
	}

	added, modified, removedIDs, nextCursor, err := s.plaid.SyncTransactions(ctx, accessToken, cursor)
	if err != nil {
		if _, statusErr := s.items.UpdateStatus(ctx, db.UpdatePlaidItemStatusParams{
			ID:     item.ID,
			Status: "error",
		}); statusErr != nil {
			// The connection stays reading "active" while it is in fact broken,
			// so both clients' bank-connection panels show it healthy and
			// nobody knows to reconnect.
			log.Printf("plaid item %s: mark item errored after sync failure: %v", item.ID, statusErr)
		}
		return result, err
	}

	log.Printf("plaid item %s: %d added, %d modified, %d removed", item.ID, len(added), len(modified), len(removedIDs))

	const variableTypeID = 2
	const oneOffFreqID = 1

	// Resolved once per Plaid account: the payment method it maps to, and the
	// name to report it under. Name is resolved even when there's no payment
	// method, so an unmapped account still shows up in the summary instead of
	// vanishing from it.
	type accountRef struct {
		paymentMethodID *uuid.UUID
		name            string
	}
	pmCache := map[string]accountRef{}
	byAccount := map[string]int{}

	fixedExpenses, _ := s.fixedExpenses.List(ctx, item.BudgetProfileID)
	aliasesByFE := make(map[uuid.UUID][]string, len(fixedExpenses))
	for _, fe := range fixedExpenses {
		aliases, _ := s.reviews.ListAliases(ctx, fe.ID)
		aliasesByFE[fe.ID] = aliases
	}

	importedAdded := 0
	autoConfirmed := 0
	queued := 0
	skippedNoPeriod := 0
	skippedDuplicate := 0
	repointed := 0

	// Read once per item rather than per transaction: it cannot change mid-run,
	// and a sync can import hundreds of rows.
	autoUpdatePlanned := autoUpdatePlannedAmountFor(ctx, s.budgets, item.BudgetProfileID, "plaid.auto_confirm")

	for _, tx := range added {
		date := pgtype.Date{Time: tx.Date, Valid: true}

		period, err := s.budgets.GetPeriodByDate(ctx, item.BudgetProfileID, date)
		if err != nil {
			skippedNoPeriod++
			log.Printf("plaid item %s: skipped %q  %s  $%.2f — no budget period covers this date (plaid_id=%s)",
				item.ID, tx.Name, tx.Date.Format("2006-01-02"), tx.Amount, tx.PlaidID)
			continue
		}

		// A pending transaction Plaid has now settled arrives here under a
		// brand-new PlaidID, linked back to the pending one only through
		// PendingTransactionID — some institutions represent settlement this
		// way instead of a same-id `modified` entry (see that field's doc
		// comment in internal/plaid/client.go). Repointing the existing local
		// row onto it, in place, is what stops the pending id's later
		// appearance in `removedIds` from being treated as an ordinary
		// delete below: transaction_review.transaction_id is ON DELETE
		// CASCADE, so deleting that row would silently drop a confirmed
		// review — and the paid fixed-expense link it recorded — with
		// nothing to explain why (issue #67). By the time the removed-ids
		// pass runs later in this same sync, this row's plaid_transaction_id
		// no longer matches the pending id, so that delete finds nothing.
		if tx.PendingTransactionID != "" {
			if existing, lookupErr := s.transactions.GetTransactionByPlaidID(ctx, &tx.PendingTransactionID); lookupErr == nil {
				if s.settlePendingTransaction(ctx, item.ID, tx, existing, autoUpdatePlanned) {
					repointed++
				}
				continue
			}
		}

		exists, err := s.transactions.ExistsTransactionByPlaidID(ctx, &tx.PlaidID)
		if err != nil || exists {
			skippedDuplicate++
			log.Printf("plaid item %s: skipped %q  %s  $%.2f — already imported (plaid_id=%s)",
				item.ID, tx.Name, tx.Date.Format("2006-01-02"), tx.Amount, tx.PlaidID)
			continue
		}

		amount := syncAmountToNumeric(tx.Amount)
		plaidID := tx.PlaidID
		periodID := period.ID

		categoryKey, categoryID := syncResolveCategoryID(tx.Name, tx.PFCPrimary, tx.PFCDetailed, categoryIDs)

		var paymentMethodID *uuid.UUID
		accountName := result.InstitutionName
		if tx.AccountID != "" {
			ref, cached := pmCache[tx.AccountID]
			if !cached {
				ref = accountRef{name: result.InstitutionName}
				pm, pmErr := s.transactions.GetPaymentMethodByPlaidAccountID(ctx, tx.AccountID)
				if pmErr == nil {
					id := pm.ID
					ref.paymentMethodID = &id
					ref.name = pm.Name
				}
				pmCache[tx.AccountID] = ref
			}
			paymentMethodID = ref.paymentMethodID
			accountName = ref.name
		}
		if accountName == "" {
			accountName = "Unknown account"
		}

		inserted, err := s.transactions.CreateTransactionFromPlaid(ctx, db.CreateTransactionFromPlaidParams{
			Name:                   &tx.Name,
			Amount:                 amount,
			PlannedAmount:          amount,
			Date:                   date,
			BudgetPeriodID:         &periodID,
			CategoryID:             categoryID,
			PaymentMethodID:        paymentMethodID,
			TransactionFrequencyID: syncInt32Ptr(oneOffFreqID),
			TransactionTypeID:      syncInt32Ptr(variableTypeID),
			PlaidTransactionID:     &plaidID,
			// Plaid's own classification, kept alongside the category we
			// resolved from it. The mapping in internal/plaid/category.go is
			// applied once here and used to be the only trace of it, which made
			// every later improvement forward-only; with the source stored, a
			// re-classification stays possible.
			PlaidPfcPrimary:      syncTextPtr(tx.PFCPrimary),
			PlaidPfcDetailed:     syncTextPtr(tx.PFCDetailed),
			PlaidReferenceNumber: syncTextPtr(tx.ReferenceNumber),
			PlaidPpdID:           syncTextPtr(tx.PPDID),
		})
		if err != nil {
			log.Printf("plaid item %s: insert tx %s: %v", item.ID, tx.PlaidID, err)
			continue
		}
		log.Printf("plaid item %s: imported %q  %s  $%.2f  category=%s", item.ID, tx.Name, tx.Date.Format("2006-01-02"), tx.Amount, syncCategoryLogValue(categoryKey, categoryID))
		importedAdded++

		// Tracks whether this transaction was consumed by the fixed-expense
		// match below. Anything left unconsumed is "newly available" and is
		// what the per-account summary counts.
		consumed := false

		bestScore, bestFE, bestAliasHit, bestAmountOK := syncScoreBestMatch(tx, categoryID, paymentMethodID, fixedExpenses, aliasesByFE)
		if bestFE == nil {
			byAccount[accountName]++
			continue
		}

		// A transaction that lands in a closed period may not settle anything:
		// marking paid and excluding are both blocked on archived periods
		// everywhere else (see docs/features/budget-list-view-rework.md), so the
		// sync must not do it either. It imports and stops.
		if period.IsArchived {
			byAccount[accountName]++
			log.Printf("plaid item %s: imported %q into archived period %s — not matching", item.ID, tx.Name, periodID)
			continue
		}

		// Scoped to this transaction's own period. Searching every live period
		// let a payment reach forward and mark the next period's bill paid
		// (issue #41).
		unpaid, upErr := s.fixedExpenses.GetUnpaidTransactionInPeriod(ctx, db.GetUnpaidTransactionByFixedExpenseInPeriodParams{
			FixedExpenseID: bestFE.ID,
			BudgetPeriodID: periodID,
		})
		hasUnpaidTarget := upErr == nil && unpaid.BudgetPeriodID != nil

		switch {
		case bestAliasHit && bestAmountOK && hasUnpaidTarget:
			// Every step below is reported as one outcome. Previously these
			// errors were discarded and the success counter/log sat outside the
			// review guard, so a half-applied match — bill marked paid, link
			// never recorded — was indistinguishable from a clean one in both
			// the run summary and the per-budget notification. That is why
			// issue #41 could only be diagnosed from the database.
			// Shared with the manual button and ConfirmTransactionReview, so
			// whether the plan follows the real cost is decided once by the
			// budget rather than three times by whichever path got there.
			if _, mErr := markFixedTransactionPaid(ctx, s.transactions, s.fixedExpenses,
				db.MarkTransactionAsPaidParams{
					ID:             unpaid.ID,
					BudgetPeriodID: *unpaid.BudgetPeriodID,
					Amount:         amount, // use actual Plaid amount, not template planned amount
					PaidDate:       date,   // the real Plaid-cleared date, not the fixed transaction's own scheduled date
				},
				autoUpdatePlanned,
				observedPayment{CategoryID: categoryID, PaymentMethodID: paymentMethodID},
				"plaid.auto_confirm",
			); mErr != nil {
				log.Printf("plaid item %s: auto-confirm %q: mark paid: %v", item.ID, tx.Name, mErr)
				byAccount[accountName]++
				continue
			}
			// Record the auto-match as a confirmed review — same as a user
			// manually confirming one from the To Review tab — so it can be
			// found and undone if the fixed expense is later unmarked as
			// paid (see UnmarkTransactionAsPaid). Exclude the imported
			// transaction from totals instead of deleting it outright, so
			// it stays visible and recoverable rather than being
			// permanently lost, matching ConfirmTransactionReview.
			review, rErr := s.reviews.Create(ctx, periodID, inserted.ID, unpaid.ID, bestScore)
			if rErr != nil {
				log.Printf("plaid item %s: auto-confirm %q: create review: %v", item.ID, tx.Name, rErr)
				byAccount[accountName]++
				continue
			}
			if uErr := s.reviews.UpdateStatus(ctx, review.ID, "confirmed"); uErr != nil {
				log.Printf("plaid item %s: auto-confirm %q: confirm review %s: %v", item.ID, tx.Name, review.ID, uErr)
				byAccount[accountName]++
				continue
			}
			if _, eErr := s.transactions.SetExcluded(ctx, db.SetTransactionExcludedParams{
				ID:             inserted.ID,
				BudgetPeriodID: periodID,
				Excluded:       true,
			}); eErr != nil {
				log.Printf("plaid item %s: auto-confirm %q: exclude import: %v", item.ID, tx.Name, eErr)
				byAccount[accountName]++
				continue
			}
			autoConfirmed++
			consumed = true
			log.Printf("plaid item %s: auto-confirmed %q (alias+amount → %q)", item.ID, tx.Name, bestFE.Name)
		case bestScore >= 80 && hasUnpaidTarget:
			if _, rErr := s.reviews.Create(ctx, periodID, inserted.ID, unpaid.ID, bestScore); rErr == nil {
				queued++
				consumed = true
				log.Printf("plaid item %s: queued review for %q (score=%.0f, fixed=%q)", item.ID, tx.Name, bestScore, bestFE.Name)
			}
		}

		if !consumed {
			byAccount[accountName]++
		}
	}

	for _, tx := range modified {
		amount := syncAmountToNumeric(tx.Amount)
		if err := s.transactions.UpdateTransactionFromPlaid(ctx, db.UpdateTransactionFromPlaidParams{
			PlaidTransactionID: &tx.PlaidID,
			Name:               &tx.Name,
			Amount:             amount,
		}); err != nil {
			log.Printf("plaid item %s: update tx %s: %v", item.ID, tx.PlaidID, err)
			continue
		}
		log.Printf("plaid item %s: updated %q  %s  $%.2f", item.ID, tx.Name, tx.Date.Format("2006-01-02"), tx.Amount)
	}

	// A pending id that settled this run was already repointed onto its
	// replacement above, before this loop runs — its plaid_transaction_id no
	// longer matches, so the delete below is a correct no-op for it rather
	// than something this loop needs to special-case.
	for _, pid := range removedIDs {
		if err := s.transactions.DeleteTransactionByPlaidID(ctx, &pid); err != nil {
			log.Printf("plaid item %s: delete tx %s: %v", item.ID, pid, err)
			continue
		}
		log.Printf("plaid item %s: removed tx %s", item.ID, pid)
	}

	_, err = s.items.UpdateSync(ctx, db.UpdatePlaidItemSyncParams{
		ID:     item.ID,
		Cursor: &nextCursor,
	})
	if err != nil {
		log.Printf("plaid item %s: update cursor: %v", item.ID, err)
	}

	result.Imported = importedAdded
	result.AutoConfirmed = autoConfirmed
	result.Queued = queued
	result.SkippedNoPeriod = skippedNoPeriod
	result.SkippedDuplicate = skippedDuplicate
	result.Modified = len(modified)
	result.Removed = len(removedIDs)
	result.Repointed = repointed
	result.ByAccount = sortedAccountImports(byAccount)

	log.Printf("plaid item %s: done — +%d imported, %d auto-confirmed, %d queued for review, %d modified, %d removed, %d repointed, %d skipped (no period), %d skipped (duplicate)",
		item.ID, importedAdded, autoConfirmed, queued, len(modified), len(removedIDs), repointed, skippedNoPeriod, skippedDuplicate)

	// A failure here means the transactions above were imported/updated/removed
	// successfully but the item's cursor wasn't advanced to reflect it — the
	// next run will re-fetch the same batch from Plaid (harmless, since
	// plaid_transaction_id dedup skips re-imports) but should still surface as
	// a failure so it isn't silently retried forever without anyone noticing.
	if err != nil {
		return result, fmt.Errorf("plaid item %s: persist cursor: %w", item.ID, err)
	}
	return result, nil
}

// settlePendingTransaction repoints existing — a transaction previously
// imported as pending — onto tx, the posted transaction that replaced it,
// instead of leaving it to the caller's normal insert-as-new path. See the
// call site in syncItemCore for why: deleting and reimporting would
// cascade-delete any transaction_review pointing at the pending row.
//
// If the repointed row is the subject of a *confirmed* review — the bill it
// paid is already marked paid, off the pending amount — and the settled
// amount differs, the paid transaction's amount is updated to match, through
// the same markFixedTransactionPaid path every other confirm route already
// shares. A merely pending (not yet confirmed) review needs nothing extra:
// its transaction_id never changes, so it already points at the refreshed
// row with no further action.
//
// Returns whether the repoint itself succeeded, for the caller's counter.
func (s *PlaidService) settlePendingTransaction(ctx context.Context, itemID uuid.UUID, tx plaidclient.Transaction, existing db.Transaction, autoUpdatePlanned bool) bool {
	repointed, err := s.transactions.RepointTransactionPlaidID(ctx, db.RepointTransactionPlaidIDParams{
		OldPlaidTransactionID: &tx.PendingTransactionID,
		NewPlaidTransactionID: &tx.PlaidID,
		Name:                  &tx.Name,
		Amount:                syncAmountToNumeric(tx.Amount),
		Date:                  pgtype.Date{Time: tx.Date, Valid: true},
	})
	if err != nil {
		log.Printf("plaid item %s: settle %q: repoint %s → %s: %v", itemID, tx.Name, tx.PendingTransactionID, tx.PlaidID, err)
		return false
	}
	log.Printf("plaid item %s: settled %q  %s  $%.2f — repointed pending transaction %s → %s",
		itemID, tx.Name, tx.Date.Format("2006-01-02"), tx.Amount, tx.PendingTransactionID, tx.PlaidID)

	review, reviewErr := s.reviews.GetByTransactionID(ctx, existing.ID)
	if reviewErr != nil || review.Status != "confirmed" {
		return true
	}
	if numericToNanos(repointed.Amount) == numericToNanos(existing.Amount) {
		return true
	}

	matchedTx, mtErr := s.transactions.GetByID(ctx, review.MatchedTransactionID)
	if mtErr != nil || matchedTx.BudgetPeriodID == nil {
		log.Printf("plaid item %s: settle %q: read matched transaction %s: %v", itemID, tx.Name, review.MatchedTransactionID, mtErr)
		return true
	}
	// Re-syncing the amount an already-confirmed bill settled at, not a fresh
	// payment event — category/payment method were already handled at the
	// original confirm, so no observedPayment override here.
	if _, paidErr := markFixedTransactionPaid(ctx, s.transactions, s.fixedExpenses,
		db.MarkTransactionAsPaidParams{
			ID:             matchedTx.ID,
			BudgetPeriodID: *matchedTx.BudgetPeriodID,
			Amount:         repointed.Amount,
			PaidDate:       matchedTx.PaidDate,
		},
		autoUpdatePlanned,
		observedPayment{},
		"plaid.settle_pending",
	); paidErr != nil {
		log.Printf("plaid item %s: settle %q: update paid amount on matched transaction %s: %v",
			itemID, tx.Name, review.MatchedTransactionID, paidErr)
		return true
	}
	log.Printf("plaid item %s: settle %q: matched transaction %s's paid amount updated to $%.2f",
		itemID, tx.Name, review.MatchedTransactionID, tx.Amount)
	return true
}

const syncAmountTolerance = 3.0

func syncAmountToNumeric(f float64) pgtype.Numeric {
	s := strconv.FormatFloat(f, 'f', 4, 64)
	var n pgtype.Numeric
	_ = n.Scan(s)
	return n
}

func syncInt32Ptr(i int32) *int32 { return &i }

// syncResolveCategory resolves the system category for an imported transaction.
// Plaid's own personal_finance_category classification (INCOME -> Income, see
// plaidclient.ResolvePlaidCategory) is the primary signal. A name containing
// "payroll" is checked first as a fallback override for accounts where Plaid
// doesn't return personal_finance_category data at all, since payroll deposits
// should never count toward the spending total either way.
// syncTextPtr stores an optional Plaid string as NULL rather than "" when
// absent, so a missing value is distinguishable from an empty one. Plaid leaves
// every payment_meta field null for anything that is not an inter-bank
// transfer, which is most transactions.
func syncTextPtr(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

func syncResolveCategory(name, pfcPrimary, pfcDetailed string) category.Key {
	if strings.Contains(strings.ToLower(name), "payroll") {
		return category.Income
	}
	return plaidclient.ResolvePlaidCategory(pfcPrimary, pfcDetailed)
}

// syncResolveCategoryID resolves a transaction's system category key and looks
// up its ID in the system-category map. A non-empty key with a nil ID means
// the resolved key has no matching system category — the transaction still
// imports, just without a category — which is otherwise invisible unless
// distinguished from a clean resolution (see syncCategoryLogValue).
func syncResolveCategoryID(txName, pfcPrimary, pfcDetailed string, categoryIDs map[category.Key]int32) (categoryKey category.Key, categoryID *int32) {
	categoryKey = syncResolveCategory(txName, pfcPrimary, pfcDetailed)
	if categoryKey == "" {
		return "", nil
	}
	if id, ok := categoryIDs[categoryKey]; ok {
		return categoryKey, &id
	}
	return categoryKey, nil
}

// syncCategoryLogValue renders the outcome of category resolution for the
// per-transaction import log. Printing the resolved name unconditionally
// (regardless of whether it actually mapped to an ID) would make an
// "unmapped" transaction — imported with no category — indistinguishable
// from a correctly categorized one in the logs.
func syncCategoryLogValue(categoryKey category.Key, categoryID *int32) string {
	switch {
	case categoryID != nil:
		return fmt.Sprintf("%q", categoryKey)
	case categoryKey != "":
		return fmt.Sprintf("%q (unmapped — no matching system category, imported without a category)", categoryKey)
	default:
		return "none"
	}
}

func syncAmountWithinTolerance(txAmount float64, fe *db.FixedExpense) bool {
	feAmt, err := fe.PlannedAmount.Float64Value()
	return err == nil && feAmt.Valid && math.Abs(txAmount-feAmt.Float64) <= syncAmountTolerance
}

// scoreBestMatch is the generic scoring core shared by both the Plaid sync
// path and manual CreateTransaction. It scores a transaction (name, amount,
// category, payment method) against each active fixed expense and returns the
// best score and the matching expense.
func scoreBestMatch(name string, amount float64, categoryID *int32, pmID *uuid.UUID, expenses []db.FixedExpense, aliasesByFE map[uuid.UUID][]string) (float64, *db.FixedExpense) {
	best := 0.0
	var bestFE *db.FixedExpense
	nameLower := strings.ToLower(name)
	for i := range expenses {
		fe := &expenses[i]
		// A card installment is settled inside the card's total balance and
		// never lands on a bank feed as its own line item, so anything scoring
		// against one of these is a false positive by construction (issue #54).
		// Skipped here rather than at the two call sites so neither can forget.
		if fe.IsInstallmentPlan {
			continue
		}
		score := 0.0
		if syncAmountWithinTolerance(amount, fe) {
			score += 40
		}
		aliasHit := false
		for _, alias := range aliasesByFE[fe.ID] {
			aliasLower := strings.ToLower(alias)
			// Exact match first (fast path, highest confidence); falling back
			// to word overlap is what lets a saved alias survive a bank
			// descriptor that embeds a changing date/reference number (e.g.
			// "Manual DB-Bkrg 09/02" vs "10/02" next month) — see
			// docs/features/transaction-review.md.
			if strings.EqualFold(alias, name) || syncNameWordsOverlap(aliasLower, nameLower) {
				aliasHit = true
				break
			}
		}
		if aliasHit || syncNameWordsOverlap(nameLower, strings.ToLower(fe.Name)) {
			score += 20
		}
		if pmID != nil && fe.PaymentMethodID != nil && *pmID == *fe.PaymentMethodID {
			score += 20
		}
		if categoryID != nil && fe.CategoryID != nil && *categoryID == *fe.CategoryID {
			score += 20
		}
		if score > best {
			best = score
			bestFE = fe
		}
	}
	return best, bestFE
}

func syncScoreBestMatch(tx plaidclient.Transaction, categoryID *int32, pmID *uuid.UUID, expenses []db.FixedExpense, aliasesByFE map[uuid.UUID][]string) (float64, *db.FixedExpense, bool, bool) {
	best := 0.0
	var bestFE *db.FixedExpense
	bestAliasHit := false
	bestAmountOK := false
	txNameLower := strings.ToLower(tx.Name)
	for i := range expenses {
		fe := &expenses[i]
		score := 0.0

		amountOK := syncAmountWithinTolerance(tx.Amount, fe)
		if amountOK {
			score += 40
		}

		aliasHit := false
		for _, alias := range aliasesByFE[fe.ID] {
			aliasLower := strings.ToLower(alias)
			// Exact match first (fast path, highest confidence); word overlap
			// is the fallback that lets a saved alias survive a bank
			// descriptor whose changing date/reference number would otherwise
			// break an exact match every single time it recurs — see
			// docs/features/transaction-review.md.
			if strings.EqualFold(alias, tx.Name) || syncNameWordsOverlap(aliasLower, txNameLower) {
				aliasHit = true
				break
			}
		}
		feLower := strings.ToLower(fe.Name)
		if aliasHit || syncNameWordsOverlap(txNameLower, feLower) {
			score += 20
		}

		if pmID != nil && fe.PaymentMethodID != nil && *pmID == *fe.PaymentMethodID {
			score += 20
		}
		if categoryID != nil && fe.CategoryID != nil && *categoryID == *fe.CategoryID {
			score += 20
		}

		if score > best {
			best = score
			bestFE = fe
			bestAliasHit = aliasHit
			bestAmountOK = amountOK
		}
	}
	return best, bestFE, bestAliasHit, bestAmountOK
}

func syncNameWordsOverlap(a, b string) bool {
	words := func(s string) map[string]struct{} {
		m := make(map[string]struct{})
		for _, w := range strings.FieldsFunc(s, func(r rune) bool { return !('a' <= r && r <= 'z') }) {
			if len(w) >= 4 {
				m[w] = struct{}{}
			}
		}
		return m
	}
	aw := words(a)
	for w := range words(b) {
		if _, ok := aw[w]; ok {
			return true
		}
	}
	return false
}
