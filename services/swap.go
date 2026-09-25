package services

import (
	"context"
	"database/sql"

	"slices"
	"time"

	"github.com/2HgO/quidax-go/errors"
	"github.com/2HgO/quidax-go/models"
	"github.com/2HgO/quidax-go/types/requests"
	"github.com/2HgO/quidax-go/types/responses"
	"github.com/2HgO/quidax-go/utils"
	sq "github.com/Masterminds/squirrel"
	"github.com/google/uuid"
	tdb "github.com/tigerbeetle/tigerbeetle-go"
	tdb_types "github.com/tigerbeetle/tigerbeetle-go/pkg/types"
	"go.uber.org/zap"
)

type InstantSwapService interface {
	CreateInstantSwap(context.Context, *requests.CreateInstantSwapRequest) (*responses.Response[*responses.InstantSwapQuotationResponseData], error)
	ConfirmInstantSwap(context.Context, *requests.ConfirmInstanSwapRequest) (*responses.Response[*responses.InstantSwapResponseData], error)
	// RefreshInstantSwap(context.Context, *requests.RefreshInstantSwapRequest) (*responses.Response[*responses.InstantSwapQuotationResponseData], error)
	FetchInstantSwapTransaction(context.Context, *requests.FetchInstantSwapTransactionRequest) (*responses.Response[*responses.InstantSwapResponseData], error)
	GetInstantSwapTransactions(context.Context, *requests.GetInstantSwapTransactionsRequest) (*responses.Response[[]*responses.InstantSwapResponseData], error)
	QuoteInstantSwap(context.Context, *requests.CreateInstantSwapRequest) (*responses.Response[*responses.QuoteInstantSwapResponseData], error)
}

func NewInstantSwapService(
	txDatabase tdb.Client,
	dataDatabase *sql.DB,
	accountService AccountService,
	walletService WalletService,
	scheduler SchedulerService,
	webhookService WebhookService,
	log *zap.Logger,
) InstantSwapService {
	return &instantSwapService{
		service{
			transactionDB:  txDatabase,
			dataDB:         dataDatabase,
			accountService: accountService,
			walletService:  walletService,
			webhookService: webhookService,
			scheduler:      scheduler,
			log:            log,
		},
	}
}

type instantSwapService struct {
	service
}

type normalizedSwapTransaction struct {
	fromToken  string
	toToken    string
	fromAmount float64
	toAmount   float64
}

func (i *instantSwapService) normalizeTransaction(from string, to string, amount float64) normalizedSwapTransaction {
	fromAmount := utils.ApproximateAmount(from, amount)
	toAmount := utils.ApproximateAmount(to, Rates[from][to]*fromAmount)

	return normalizedSwapTransaction{
		fromToken:  from,
		toToken:    to,
		fromAmount: fromAmount,
		toAmount:   toAmount,
	}
}

func (i *instantSwapService) QuoteInstantSwap(ctx context.Context, req *requests.CreateInstantSwapRequest) (*responses.Response[*responses.QuoteInstantSwapResponseData], error) {
	transactionDetails := i.normalizeTransaction(req.FromCurrency, req.ToCurrency, float64(req.FromAmount))

	data := &responses.QuoteInstantSwapResponseData{
		FromCurrency:   req.FromCurrency,
		ToCurrency:     req.ToCurrency,
		QuotedPrice:    utils.ApproximateAmount(req.ToCurrency, Rates[req.FromCurrency][req.ToCurrency]),
		QuotedCurrency: req.FromCurrency,
		FromAmount:     transactionDetails.fromAmount,
		ToAmount:       transactionDetails.toAmount,
	}
	if req.FromCurrency == "ngn" {
		data.QuotedCurrency = req.FromCurrency
		data.QuotedPrice = utils.ApproximateAmount(req.FromCurrency, 1/Rates[req.FromCurrency][req.ToCurrency])
	}

	return &responses.Response[*responses.QuoteInstantSwapResponseData]{
		Status: "success",
		Data:   data,
	}, nil
}

func (i *instantSwapService) CreateInstantSwap(ctx context.Context, req *requests.CreateInstantSwapRequest) (*responses.Response[*responses.InstantSwapQuotationResponseData], error) {
	transactionDetails := i.normalizeTransaction(req.FromCurrency, req.ToCurrency, float64(req.FromAmount))
	fromWallet, err := i.walletService.FetchUserWallet(ctx, &requests.FetchUserWalletRequest{UserID: req.UserID, Currency: req.FromCurrency})
	if err != nil {
		return nil, err
	}
	fromWalletID, err := tdb_types.HexStringToUint128(fromWallet.Data.ID)
	if err != nil {
		return nil, err
	}
	toWallet, err := i.walletService.FetchUserWallet(ctx, &requests.FetchUserWalletRequest{UserID: req.UserID, Currency: req.ToCurrency})
	if err != nil {
		return nil, err
	}
	toWalletID, err := tdb_types.HexStringToUint128(toWallet.Data.ID)
	if err != nil {
		return nil, err
	}

	tx, err := i.dataDB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	// Defer a rollback in case anything fails.
	defer tx.Rollback()

	quoteTxID0 := tdb_types.ID()
	quoteTxID1 := tdb_types.ID()
	swap := &models.InstantSwap{
		ID:            uuid.NewString(),
		QuotationID:   uuid.NewString(),
		FromWalletID:  fromWallet.Data.ID,
		ToWalletID:    toWallet.Data.ID,
		QuotationRate: utils.ApproximateAmount(req.ToCurrency, Rates[req.FromCurrency][req.ToCurrency]),
		SwapTxID0:     tdb_types.ID().String(),
		SwapTxID1:     tdb_types.ID().String(),
		QuoteTxID0:    quoteTxID0.String(),
		QuoteTxID1:    quoteTxID1.String(),
	}
	if req.FromCurrency == "ngn" {
		swap.QuotationRate = utils.ApproximateAmount(req.FromCurrency, 1/Rates[req.FromCurrency][req.ToCurrency])
	}
	swap.ExecutionRate = swap.QuotationRate

	_, err = sq.
		Insert("instant_swaps").
		Columns("id", "quotation_id", "from_wallet_id", "to_wallet_id", "quotation_rate", "execution_rate", "swap_tx_id_0", "swap_tx_id_1", "quote_tx_id_0", "quote_tx_id_1").
		Values(swap.ID, swap.QuotationID, swap.FromWalletID, swap.ToWalletID, swap.QuotationRate, swap.ExecutionRate, swap.SwapTxID0, swap.SwapTxID1, swap.QuoteTxID0, swap.QuoteTxID1).
		RunWith(tx).ExecContext(ctx)

	if err != nil {
		return nil, errors.HandleDataDBError(err)
	}

	now := time.Now()
	timeout := now.Add(time.Second * 12)
	transactions := []tdb_types.Transfer{
		{
			ID:              quoteTxID0,
			CreditAccountID: tdb_types.ToUint128(uint64(LedgerIDs[req.FromCurrency])),
			DebitAccountID:  fromWalletID,
			Amount:          utils.ToAmount(transactionDetails.fromAmount),
			UserData128:     tdb_types.BytesToUint128(uuid.MustParse(fromWallet.Data.User.ID)),
			Ledger:          LedgerIDs[req.FromCurrency],
			Code:            1,
			Flags: tdb_types.TransferFlags{
				Linked:  true,
				Pending: true,
			}.ToUint16(),
		},
		{
			ID:              quoteTxID1,
			DebitAccountID:  tdb_types.ToUint128(uint64(LedgerIDs[req.ToCurrency])),
			CreditAccountID: toWalletID,
			Amount:          utils.ToAmount(transactionDetails.toAmount),
			Ledger:          LedgerIDs[req.ToCurrency],
			UserData128:     tdb_types.BytesToUint128(uuid.MustParse(toWallet.Data.User.ID)),
			Code:            1,
			Flags: tdb_types.TransferFlags{
				Pending: true,
			}.ToUint16(),
		},
	}

	res, err := i.transactionDB.CreateTransfers(transactions)
	if err != nil {
		return nil, errors.HandleTxDBError(err)
	}
	if len(res) > 0 {
		for _, r := range res {
			if r.Result == tdb_types.TransferExceedsCredits {
				return nil, errors.NewFailedDependencyError("Insufficient Balance")
			}
		}
		return nil, errors.NewFailedDependencyError(res[0].Result.String())
	}

	if err = tx.Commit(); err != nil {
		return nil, errors.HandleDataDBError(err)
	}

	data := &responses.InstantSwapQuotationResponseData{
		ID:             swap.QuotationID,
		FromCurrency:   req.FromCurrency,
		ToCurrency:     req.ToCurrency,
		QuotedPrice:    swap.QuotationRate,
		QuotedCurrency: req.ToCurrency,
		FromAmount:     transactionDetails.fromAmount,
		ToAmount:       transactionDetails.toAmount,
		Confirmed:      false,
		ExpiresAt:      timeout,
		CreatedAt:      now,
		User:           fromWallet.Data.User,
	}
	if data.FromCurrency == "ngn" {
		data.QuotedCurrency = req.FromCurrency
	}

	parent := ctx.Value("user").(*models.Account)
	i.scheduler.ScheduleInstantSwapReversal(swap.QuotationID, now.Add(time.Second*12))
	go i.webhookService.SendWalletUpdatedEvent(parent.WebhookDetails, fromWallet.Data)

	return &responses.Response[*responses.InstantSwapQuotationResponseData]{
		Status:  "success",
		Message: "Successful",
		Data:    data,
	}, nil
}

func (i *instantSwapService) ConfirmInstantSwap(ctx context.Context, req *requests.ConfirmInstanSwapRequest) (*responses.Response[*responses.InstantSwapResponseData], error) {
	user, err := i.accountService.FetchAccountDetails(ctx, &requests.FetchAccountDetailsRequest{UserID: req.UserID})
	if err != nil {
		return nil, err
	}

	row := sq.
		Select("instant_swaps.id", "quotation_id", "from_wallet_id", "to_wallet_id", "quotation_rate", "execution_rate", "swap_tx_id_0", "swap_tx_id_1", "quote_tx_id_0", "quote_tx_id_1").
		From("instant_swaps").
		Where(sq.Eq{"quotation_id": req.QuotationID}).
		RunWith(i.dataDB).
		QueryRow()
	if row == nil {
		return nil, errors.NewNotFoundError("swap quotation not found")
	}

	var swap models.InstantSwap
	err = row.Scan(
		&swap.ID,
		&swap.QuotationID,
		&swap.FromWalletID,
		&swap.ToWalletID,
		&swap.QuotationRate,
		&swap.ExecutionRate,
		&swap.SwapTxID0,
		&swap.SwapTxID1,
		&swap.QuoteTxID0,
		&swap.QuoteTxID1,
	)
	if err != nil {
		return nil, errors.HandleDataDBError(err)
	}

	qtx0, _ := tdb_types.HexStringToUint128(swap.QuoteTxID0)
	qtx1, _ := tdb_types.HexStringToUint128(swap.QuoteTxID1)
	transactions, err := i.transactionDB.LookupTransfers([]tdb_types.Uint128{qtx0, qtx1})
	if err != nil {
		return nil, errors.HandleTxDBError(err)
	}
	if len(transactions) != 2 {
		return nil, errors.NewFailedDependencyError("transaction not found")
	}

	now := time.Now()

	go i.processSwap(swap, now, transactions)

	return &responses.Response[*responses.InstantSwapResponseData]{
		Status:  "success",
		Message: "Successful",
		Data: &responses.InstantSwapResponseData{
			ID:             swap.ID,
			FromCurrency:   Ledgers[transactions[0].Ledger],
			ToCurrency:     Ledgers[transactions[1].Ledger],
			ExecutionPrice: utils.Formatter.Sprintf("%f", swap.ExecutionRate),
			FromAmount:     utils.ApproximateAmount(Ledgers[transactions[0].Ledger], utils.FromAmount(transactions[0].Amount)),
			ReceivedAmount: utils.ApproximateAmount(Ledgers[transactions[1].Ledger], utils.FromAmount(transactions[1].Amount)),
			CreatedAt:      now,
			UpdatedAt:      now,
			User:           user.Data,
			Status:         "pending",
			SwapQuotation: &responses.InstantSwapQuotationResponseData{
				ID:             swap.QuotationID,
				FromCurrency:   Ledgers[transactions[0].Ledger],
				ToCurrency:     Ledgers[transactions[1].Ledger],
				QuotedPrice:    swap.QuotationRate,
				QuotedCurrency: Ledgers[transactions[1].Ledger],
				FromAmount:     utils.ApproximateAmount(Ledgers[transactions[0].Ledger], utils.FromAmount(transactions[0].Amount)),
				ToAmount:       utils.ApproximateAmount(Ledgers[transactions[1].Ledger], utils.FromAmount(transactions[1].Amount)),
				Confirmed:      true,
				ExpiresAt:      time.UnixMicro(int64(transactions[0].Timestamp / 1000)).Add(time.Second * 12),
				CreatedAt:      time.UnixMicro(int64(transactions[0].Timestamp / 1000)),
				User:           user.Data,
			},
		},
	}, nil
}

func (i *instantSwapService) processSwap(swap models.InstantSwap, ts time.Time, transactions []tdb_types.Transfer) {
	failed := utils.FromAmount(transactions[0].Amount) > 500000

	user, err := i.accountService.FetchAccountDetails(context.WithValue(context.Background(), "skip_check", true), &requests.FetchAccountDetailsRequest{UserID: uuid.UUID(transactions[0].UserData128.Bytes()).String()})
	if err != nil {
		i.log.Error("fetching user details for instant swap processing", zap.Error(err))
		return
	}

	stx0, _ := tdb_types.HexStringToUint128(swap.SwapTxID0)
	stx1, _ := tdb_types.HexStringToUint128(swap.SwapTxID1)
	fromAmount := utils.FromAmount(transactions[0].Amount)
	toAmount := utils.FromAmount(transactions[1].Amount)
	confirmedTransactions := []tdb_types.Transfer{
		{
			ID:              stx0,
			CreditAccountID: transactions[0].CreditAccountID,
			DebitAccountID:  transactions[0].DebitAccountID,
			Amount:          transactions[0].Amount,
			Ledger:          transactions[0].Ledger,
			UserData128:     transactions[0].UserData128,
			PendingID:       transactions[0].ID,
			Code:            1,
			Flags: tdb_types.TransferFlags{
				Linked:              true,
				PostPendingTransfer: utils.FromAmount(transactions[0].Amount) <= 500000,
				VoidPendingTransfer: utils.FromAmount(transactions[0].Amount) > 500000,
			}.ToUint16(),
		},
		{
			ID:              stx1,
			CreditAccountID: transactions[1].CreditAccountID,
			DebitAccountID:  transactions[1].DebitAccountID,
			Amount:          transactions[1].Amount,
			Ledger:          transactions[1].Ledger,
			UserData128:     transactions[1].UserData128,
			PendingID:       transactions[1].ID,
			Code:            1,
			Flags: tdb_types.TransferFlags{
				PostPendingTransfer: utils.FromAmount(transactions[0].Amount) <= 500000,
				VoidPendingTransfer: utils.FromAmount(transactions[0].Amount) > 500000,
			}.ToUint16(),
		},
	}

	res, err := i.transactionDB.CreateTransfers(confirmedTransactions)
	if err != nil {
		i.log.Error("processing swap transaction", zap.Error(err))
		return
	}

	if len(res) > 0 {
		for _, r := range res {
			if r.Result == tdb_types.TransferExceedsCredits {
				for i := range confirmedTransactions {
					confirmedTransactions[i].Flags = tdb_types.TransferFlags{
						Linked:              i == 0,
						VoidPendingTransfer: true,
					}.ToUint16()
				}
				res, err = i.transactionDB.CreateTransfers(confirmedTransactions)
				if err != nil {
					i.log.Error("failing swap transaction", zap.Error(err))
				}
				failed = true
				goto failedTransfer
			}
			i.log.Error("processing swap transaction", zap.String("info", r.Result.String()))
		}
		// ?todo: retry transfer confirmation?
		return
	}

failedTransfer:
	data := &responses.InstantSwapResponseData{
		ID:             swap.ID,
		FromCurrency:   Ledgers[transactions[0].Ledger],
		ToCurrency:     Ledgers[transactions[1].Ledger],
		ExecutionPrice: utils.Formatter.Sprintf("%f", swap.ExecutionRate),
		FromAmount:     utils.ApproximateAmount(Ledgers[transactions[0].Ledger], fromAmount),
		ReceivedAmount: utils.ApproximateAmount(Ledgers[transactions[1].Ledger], toAmount),
		CreatedAt:      ts,
		UpdatedAt:      ts,
		User:           user.Data,
		Status:         "confirmed",
		SwapQuotation: &responses.InstantSwapQuotationResponseData{
			ID:             swap.QuotationID,
			FromCurrency:   Ledgers[transactions[0].Ledger],
			ToCurrency:     Ledgers[transactions[1].Ledger],
			QuotedPrice:    swap.QuotationRate,
			QuotedCurrency: Ledgers[transactions[1].Ledger],
			FromAmount:     utils.ApproximateAmount(Ledgers[transactions[0].Ledger], fromAmount),
			ToAmount:       utils.ApproximateAmount(Ledgers[transactions[1].Ledger], toAmount),
			Confirmed:      true,
			ExpiresAt:      time.UnixMicro(int64(transactions[0].Timestamp / 1000)).Add(time.Second * 12),
			CreatedAt:      time.UnixMicro(int64(transactions[0].Timestamp / 1000)),
			User:           user.Data,
		},
	}

	switch failed {
	case true:
		data.Status = "error"
		i.webhookService.
			SendInstantSwapFailedEvent(user.Data.WebhookDetails, data)

		// todo: send wallet updated event for debit wallet
	default:
		data.Status = "success"
		i.webhookService.
			SendInstantSwapCompletedEvent(user.Data.WebhookDetails, data)

		// todo: send wallet updated event for credit and debit wallets
	}
}

func (i *instantSwapService) FetchInstantSwapTransaction(ctx context.Context, req *requests.FetchInstantSwapTransactionRequest) (*responses.Response[*responses.InstantSwapResponseData], error) {
	user, err := i.accountService.FetchAccountDetails(ctx, &requests.FetchAccountDetailsRequest{UserID: req.UserID})
	if err != nil {
		return nil, err
	}

	row := sq.
		Select("instant_swaps.id", "quotation_id", "from_wallet_id", "to_wallet_id", "quotation_rate", "execution_rate", "swap_tx_id_0", "swap_tx_id_1", "quote_tx_id_0", "quote_tx_id_1").
		From("instant_swaps").
		Where(sq.Eq{"id": req.SwapTransactionID}).
		RunWith(i.dataDB).
		QueryRowContext(ctx)
	var swap models.InstantSwap
	err = row.Scan(
		&swap.ID,
		&swap.QuotationID,
		&swap.FromWalletID,
		&swap.ToWalletID,
		&swap.QuotationRate,
		&swap.ExecutionRate,
		&swap.SwapTxID0,
		&swap.SwapTxID1,
		&swap.QuoteTxID0,
		&swap.QuoteTxID1,
	)
	if err != nil {
		return nil, errors.HandleDataDBError(err)
	}

	stx0, _ := tdb_types.HexStringToUint128(swap.SwapTxID0)
	stx1, _ := tdb_types.HexStringToUint128(swap.SwapTxID1)
	swaps, err := i.transactionDB.LookupTransfers([]tdb_types.Uint128{stx0, stx1})
	if err != nil {
		return nil, errors.HandleTxDBError(err)
	}

	qtx0, _ := tdb_types.HexStringToUint128(swap.QuoteTxID0)
	qtx1, _ := tdb_types.HexStringToUint128(swap.QuoteTxID1)
	quotes, err := i.transactionDB.LookupTransfers([]tdb_types.Uint128{qtx0, qtx1})
	if err != nil {
		return nil, errors.HandleTxDBError(err)
	}

	if len(swaps)+len(quotes) < 4 {
		return nil, errors.NewNotFoundError("swap not found")
	}

	data, err := i.groupTransactions(slices.Concat(swaps, quotes), user.Data, swap)
	if err != nil {
		return nil, err
	}

	return &responses.Response[*responses.InstantSwapResponseData]{
		Status:  "success",
		Message: "Successful",
		Data:    data[0],
	}, nil
}

func (i *instantSwapService) GetInstantSwapTransactions(ctx context.Context, req *requests.GetInstantSwapTransactionsRequest) (*responses.Response[[]*responses.InstantSwapResponseData], error) {
	user, err := i.accountService.FetchAccountDetails(ctx, &requests.FetchAccountDetailsRequest{UserID: req.UserID})
	if err != nil {
		return nil, err
	}

	rows, err := sq.
		Select("instant_swaps.id", "quotation_id", "from_wallet_id", "to_wallet_id", "quotation_rate", "execution_rate", "swap_tx_id_0", "swap_tx_id_1", "quote_tx_id_0", "quote_tx_id_1").
		From("instant_swaps").
		Join("wallets on wallets.id = instant_swaps.from_wallet_id").
		Where(sq.Eq{"wallets.account_id": user.Data.ID}).
		RunWith(i.dataDB).
		QueryContext(ctx)
	if err != nil {
		return nil, errors.HandleDataDBError(err)
	}
	var swaps = []models.InstantSwap{}
	var swapIds = []tdb_types.Uint128{}
	var quoteIds = []tdb_types.Uint128{}
	for rows.Next() {
		var swap = models.InstantSwap{}
		err = rows.Scan(
			&swap.ID,
			&swap.QuotationID,
			&swap.FromWalletID,
			&swap.ToWalletID,
			&swap.QuotationRate,
			&swap.ExecutionRate,
			&swap.SwapTxID0,
			&swap.SwapTxID1,
			&swap.QuoteTxID0,
			&swap.QuoteTxID1,
		)
		if err != nil {
			return nil, errors.HandleDataDBError(err)
		}

		stx0, _ := tdb_types.HexStringToUint128(swap.SwapTxID0)
		stx1, _ := tdb_types.HexStringToUint128(swap.SwapTxID1)
		qtx0, _ := tdb_types.HexStringToUint128(swap.QuoteTxID0)
		qtx1, _ := tdb_types.HexStringToUint128(swap.QuoteTxID1)

		swaps = append(swaps, swap)
		swapIds = append(swapIds, stx0, stx1)
		quoteIds = append(quoteIds, qtx0, qtx1)
	}

	transfers, err := i.transactionDB.LookupTransfers(swapIds)
	if err != nil {
		return nil, errors.HandleTxDBError(err)
	}
	pendingTxs, err := i.transactionDB.LookupTransfers(quoteIds)
	if err != nil {
		return nil, errors.HandleTxDBError(err)
	}

	data, err := i.groupTransactions(slices.Concat(transfers, pendingTxs), user.Data, swaps...)
	if err != nil {
		return nil, err
	}

	return &responses.Response[[]*responses.InstantSwapResponseData]{
		Status:  "success",
		Message: "Successful",
		Data:    data,
	}, nil
}

func (i *instantSwapService) groupTransactions(txs []tdb_types.Transfer, user *models.Account, swaps ...models.InstantSwap) ([]*responses.InstantSwapResponseData, error) {
	result := make([]*responses.InstantSwapResponseData, 0, len(swaps))
	swapMap := map[string]tdb_types.Transfer{}
	quoteMap := map[string]tdb_types.Transfer{}
	for _, tx := range txs {
		switch {
		case tx.TransferFlags().Pending:
			quoteMap[tx.ID.String()] = tx
		default:
			swapMap[tx.ID.String()] = tx
		}
	}

	for _, swap := range swaps {
		status := "pending"
		stx0, ok1 := swapMap[swap.SwapTxID0]
		stx1, ok2 := swapMap[swap.SwapTxID1]

		qtx0 := quoteMap[swap.QuoteTxID0]
		qtx1 := quoteMap[swap.QuoteTxID1]
		if ok1 && ok2 {
			switch {
			case stx0.TransferFlags().PostPendingTransfer:
				status = "confirmed"
			case time.UnixMicro(int64(qtx0.Timestamp / 1000)).Add(time.Second * 12).Before(time.UnixMicro(int64(stx0.Timestamp / 1000))):
				status = "reversed"
			default:
				status = "failed"
			}
		}
		data := &responses.InstantSwapResponseData{
			ID:             swap.ID,
			FromCurrency:   Ledgers[qtx0.Ledger],
			ToCurrency:     Ledgers[qtx1.Ledger],
			ExecutionPrice: utils.Formatter.Sprintf("%f", swap.ExecutionRate),
			FromAmount:     utils.ApproximateAmount(Ledgers[qtx0.Ledger], utils.FromAmount(qtx0.Amount)),
			ReceivedAmount: utils.ApproximateAmount(Ledgers[qtx1.Ledger], utils.FromAmount(qtx1.Amount)),
			CreatedAt:      time.UnixMicro(int64(qtx0.Timestamp / 1000)),
			UpdatedAt:      time.UnixMicro(int64(qtx0.Timestamp / 1000)),
			User:           user,
			Status:         status,
			SwapQuotation: &responses.InstantSwapQuotationResponseData{
				ID:             swap.QuotationID,
				FromCurrency:   Ledgers[qtx0.Ledger],
				ToCurrency:     Ledgers[qtx1.Ledger],
				QuotedPrice:    swap.QuotationRate,
				QuotedCurrency: Ledgers[qtx1.Ledger],
				FromAmount:     utils.ApproximateAmount(Ledgers[qtx0.Ledger], utils.FromAmount(qtx0.Amount)),
				ToAmount:       utils.ApproximateAmount(Ledgers[qtx1.Ledger], utils.FromAmount(qtx1.Amount)),
				Confirmed:      status != "reversed",
				ExpiresAt:      time.UnixMicro(int64(qtx0.Timestamp / 1000)).Add(12 * time.Second),
				CreatedAt:      time.UnixMicro(int64(qtx0.Timestamp / 1000)),
				User:           user,
			},
		}
		if ok1 && ok2 {
			data.FromAmount = utils.ApproximateAmount(Ledgers[stx0.Ledger], utils.FromAmount(stx0.Amount))
			data.ReceivedAmount = utils.ApproximateAmount(Ledgers[stx1.Ledger], utils.FromAmount(stx1.Amount))
			data.CreatedAt = time.UnixMicro(int64(stx0.Timestamp / 1000))
			data.UpdatedAt = time.UnixMicro(int64(stx0.Timestamp / 1000))
		}
		if data.FromCurrency == "ngn" {
			data.SwapQuotation.QuotedCurrency = data.FromCurrency
		}

		result = append(result, data)
	}

	return result, nil
}
