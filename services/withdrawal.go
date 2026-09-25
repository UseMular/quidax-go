package services

import (
	"context"
	"database/sql"
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

type WithdrawalService interface {
	CreateUserWithdrawal(context.Context, *requests.CreateWithdrawalRequest) (*responses.Response[*responses.WithdrawalResponseData], error)
	FetchWithdrawal(context.Context, *requests.FetchWithdrawalRequest) (*responses.Response[*responses.WithdrawalResponseData], error)
	FetchWithdrawals(context.Context, *requests.FetchWithdrawalsRequest) (*responses.Response[[]*responses.WithdrawalResponseData], error)
}

func NewWithdrawalService(txDatabase tdb.Client, dataDatabase *sql.DB, accountService AccountService, walletService WalletService, webhookService WebhookService, depositService DepositService, log *zap.Logger) WithdrawalService {
	return &withdrawalService{
		service{
			transactionDB:  txDatabase,
			dataDB:         dataDatabase,
			accountService: accountService,
			depositService: depositService,
			walletService:  walletService,
			webhookService: webhookService,
			log:            log,
		},
	}
}

type withdrawalService struct {
	service
}

func (w *withdrawalService) CreateUserWithdrawal(ctx context.Context, req *requests.CreateWithdrawalRequest) (*responses.Response[*responses.WithdrawalResponseData], error) {
	amount := utils.ApproximateAmount(req.Currency, float64(req.Amount))
	wallet, err := w.walletService.FetchUserWallet(ctx, &requests.FetchUserWalletRequest{UserID: req.UserID, Currency: req.Currency})
	if err != nil {
		return nil, err
	}
	walletID, err := tdb_types.HexStringToUint128(wallet.Data.ID)
	if err != nil {
		return nil, err
	}
	destination, err := w.walletService.FetchUserWallet(context.WithValue(ctx, "skip_check", true), &requests.FetchUserWalletRequest{UserID: req.FundUid, Currency: req.Currency})
	// if err != nil {
	// 	return nil, err
	// }

	txID := tdb_types.ID()
	txID2 := tdb_types.ID()
	id := uuid.New()
	var withdrawal *models.Withdrawal
	if destination == nil || destination.Status != "successful" {
		w.log.Error("Error fetching internal destination", zap.Any("req", req), zap.Error(err))
		withdrawal = &models.Withdrawal{
			ID:              id.String(),
			WalletID:        wallet.Data.ID,
			Ref:             txID.String(), // ref == tx_id for all internal withdrawals
			TxID:            txID.String(),
			TransactionNote: req.TransactionNote,
			Narration:       req.Narration,
			Status:          models.Completed_WithdrawalStatus,
			Recipient: &models.Recipient{
				Type: models.CoinAddress_RecipientType,
				Details: &models.RecipientDetails{
					Address: &req.FundUid,
				},
			},
		}
	} else {
		withdrawal = &models.Withdrawal{
			ID:              id.String(),
			WalletID:        wallet.Data.ID,
			Ref:             txID.String(), // ref == tx_id for all internal withdrawals
			TxID:            txID.String(),
			TransactionNote: req.TransactionNote,
			Narration:       req.Narration,
			Status:          models.Completed_WithdrawalStatus,
			Recipient: &models.Recipient{
				Type: models.Internal_RecipientType,
				Details: &models.RecipientDetails{
					Name:           utils.String(destination.Data.User.FirstName),
					DestinationTag: utils.String(destination.Data.User.ID),
				},
			},
		}
	}

	tx, err := w.dataDB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	// Defer a rollback in case anything fails.
	defer tx.Rollback()

	_, err = sq.
		Insert("withdrawals").
		Columns(
			"id", "wallet_id", "ref", "tx_id", "transaction_note", "narration",
			"status", "recipient_type", "recipient_details_name",
			"recipient_details_destination_tag", "recipient_details_address",
		).
		Values(
			withdrawal.ID, withdrawal.WalletID, withdrawal.Ref, withdrawal.TxID, withdrawal.TransactionNote, withdrawal.Narration,
			withdrawal.Status, withdrawal.Recipient.Type, withdrawal.Recipient.Details.Name,
			withdrawal.Recipient.Details.DestinationTag, withdrawal.Recipient.Details.Address,
		).
		RunWith(tx).
		ExecContext(ctx)

		// TODO: create corresponding `internal` deposit for recipient wallet

	if err != nil {
		return nil, err
	}

	now := time.Now()
	isExternal := false
	if destination == nil || destination.Status != "successful" {
		trf := tdb_types.Transfer{
			ID:              txID,
			DebitAccountID:  walletID,
			CreditAccountID: tdb_types.ToUint128(uint64(LedgerIDs[req.Currency])),
			Amount:          utils.ToAmount(amount),
			Ledger:          LedgerIDs[req.Currency],
			UserData128:     tdb_types.BytesToUint128(uuid.MustParse(wallet.Data.User.ID)),
			Code:            3,
		}
		_, err := w.transactionDB.CreateTransfers([]tdb_types.Transfer{trf})
		if err != nil {
			return nil, errors.HandleTxDBError(err)
		}
	} else {
		destinationID, err := tdb_types.HexStringToUint128(destination.Data.ID)
		if err != nil {
			return nil, err
		}
		var transfers []tdb_types.Transfer
		if (destination.Data.User.ParentID != nil && wallet.Data.User.ParentID != nil && *destination.Data.User.ParentID == *wallet.Data.User.ParentID) ||
			(destination.Data.User.ParentID != nil && *destination.Data.User.ParentID == wallet.Data.User.ID) ||
			(wallet.Data.User.ParentID != nil && *wallet.Data.User.ParentID == destination.Data.User.ID) {
			transfers = []tdb_types.Transfer{
				{
					ID:              txID,
					DebitAccountID:  walletID,
					CreditAccountID: destinationID,
					Amount:          utils.ToAmount(amount),
					Ledger:          LedgerIDs[req.Currency],
					UserData128:     tdb_types.BytesToUint128(uuid.MustParse(wallet.Data.User.ID)),
					Code:            2,
				},
			}
		} else {
			isExternal = true
			transfers = []tdb_types.Transfer{
				{
					ID:              txID,
					DebitAccountID:  walletID,
					CreditAccountID: tdb_types.ToUint128(uint64(LedgerIDs[req.Currency])),
					Amount:          utils.ToAmount(amount),
					Ledger:          LedgerIDs[req.Currency],
					UserData128:     tdb_types.BytesToUint128(uuid.MustParse(wallet.Data.User.ID)),
					Code:            3,
				},
				{
					ID:              txID2,
					DebitAccountID:  tdb_types.ToUint128(uint64(LedgerIDs[req.Currency])),
					CreditAccountID: destinationID,
					Amount:          utils.ToAmount(amount),
					Ledger:          LedgerIDs[req.Currency],
					UserData128:     tdb_types.BytesToUint128(uuid.MustParse(destination.Data.User.ID)),
					Code:            3,
				},
			}
		}
		res, err := w.transactionDB.CreateTransfers(transfers)
		if err != nil {
			return nil, errors.HandleTxDBError(err)
		}
		if len(res) > 0 {
			for _, r := range res {
				if r.Result == tdb_types.TransferExceedsCredits {
					return nil, errors.NewFailedDependencyError("Insufficient Balance")
				}
			}
			return nil, errors.NewUnknownError(res[0].Result.String())
		}
	}

	if err = tx.Commit(); err != nil {
		return nil, errors.HandleDataDBError(err)
	}

	if destination != nil && destination.Status == "successful" && isExternal {
		data, err := w.depositService.FetchDeposit(context.WithValue(ctx, "skip_check", true), &requests.FetchDepositRequest{
			UserID:        destination.Data.User.ID,
			TransactionID: txID2.String(),
		})
		if err == nil {
			go w.webhookService.SendDepositSuccessfulEvent(destination.Data.User.WebhookDetails, data.Data)
		}
	}

	data := &responses.WithdrawalResponseData{
		ID:              withdrawal.ID,
		Reference:       withdrawal.Ref,
		Type:            withdrawal.Recipient.Type,
		Currency:        req.Currency,
		Amount:          amount,
		Fee:             0,
		Total:           amount,
		TransactionID:   withdrawal.TxID,
		TransactionNote: withdrawal.TransactionNote,
		Narration:       withdrawal.Narration,
		Status:          withdrawal.Status,
		CreatedAt:       now,
		DoneAt:          now,
		Recipient:       withdrawal.Recipient,
		Wallet:          wallet.Data,
		User:            wallet.Data.User,
	}
	go w.webhookService.SendWithdrawalSuccessfulEvent(ctx.Value("user").(*models.Account).WebhookDetails, data)

	return &responses.Response[*responses.WithdrawalResponseData]{
		Data: data,
	}, nil
}

func (w *withdrawalService) FetchWithdrawal(ctx context.Context, req *requests.FetchWithdrawalRequest) (*responses.Response[*responses.WithdrawalResponseData], error) {
	user, err := w.accountService.FetchAccountDetails(ctx, &requests.FetchAccountDetailsRequest{UserID: req.UserID})
	if err != nil {
		return nil, err
	}

	stmt := sq.
		Select(
			"withdrawals.id", "withdrawals.ref", "withdrawals.tx_id", "withdrawals.transaction_note",
			"withdrawals.narration", "withdrawals.status", "withdrawals.recipient_type", "withdrawals.recipient_details_name",
			"withdrawals.recipient_details_destination_tag", "withdrawals.recipient_details_address",

			"wallets.id",
		).
		From("withdrawals").
		Join("wallets on withdrawals.wallet_id = wallets.id").
		Where(sq.Or{sq.Eq{"wallets.account_id": user.Data.ID}, sq.Eq{"withdrawals.recipient_details_destination_tag": user.Data.ID}})

	switch "" {
	case req.Reference:
		stmt = stmt.Where(sq.Eq{"withdrawals.id": req.WithdrawalID})
	case req.WithdrawalID:
		stmt = stmt.Where(sq.Eq{"withdrawals.ref": req.Reference})
	default:
		panic("unreachable")
	}

	row := stmt.RunWith(w.dataDB).QueryRowContext(ctx)

	withdrawal := &responses.WithdrawalResponseData{
		Recipient: &models.Recipient{
			Details: &models.RecipientDetails{},
		},
		Wallet: &responses.UserWalletResponseData{},
		User:   &models.Account{},
	}
	err = row.Scan(
		&withdrawal.ID, &withdrawal.Reference, &withdrawal.TransactionID, &withdrawal.TransactionNote,
		&withdrawal.Narration, &withdrawal.Status, &withdrawal.Recipient.Type, &withdrawal.Recipient.Details.Name,
		&withdrawal.Recipient.Details.DestinationTag, &withdrawal.Recipient.Details.Address,

		&withdrawal.Wallet.ID,
	)
	if err != nil {
		return nil, errors.HandleDataDBError(err)
	}

	data, err := w.populateWithdrawals(ctx, map[string]*responses.WithdrawalResponseData{withdrawal.TransactionID: withdrawal}, user.Data)
	if err != nil {
		return nil, err
	}

	if len(data) != 1 {
		return nil, errors.NewNotFoundError("withdrawal not found")
	}

	return &responses.Response[*responses.WithdrawalResponseData]{
		Status: "successful",
		Data:   data[0],
	}, nil
}

func (w *withdrawalService) FetchWithdrawals(ctx context.Context, req *requests.FetchWithdrawalsRequest) (*responses.Response[[]*responses.WithdrawalResponseData], error) {
	user, err := w.accountService.FetchAccountDetails(ctx, &requests.FetchAccountDetailsRequest{UserID: req.UserID})
	if err != nil {
		return nil, err
	}

	stmt := sq.
		Select(
			"withdrawals.id", "withdrawals.ref", "withdrawals.tx_id", "withdrawals.transaction_note",
			"withdrawals.narration", "withdrawals.status", "withdrawals.recipient_type", "withdrawals.recipient_details_name",
			"withdrawals.recipient_details_destination_tag", "withdrawals.recipient_details_address",

			"wallets.id",
		).
		From("withdrawals").
		Join("wallets on withdrawals.wallet_id = wallets.id").
		Where(sq.Or{sq.Eq{"wallets.account_id": user.Data.ID}, sq.Eq{"withdrawals.recipient_details_destination_tag": user.Data.ID}})

	if req.State != nil {
		stmt = stmt.Where(sq.Eq{"withdrawals.state": *req.State})
	}
	if req.Currency != nil {
		stmt = stmt.Where(sq.Eq{"wallets.token": *req.Currency})
	}

	rows, err := stmt.RunWith(w.dataDB).QueryContext(ctx)
	if err != nil {
		return nil, errors.HandleDataDBError(err)
	}

	withdrawals := map[string]*responses.WithdrawalResponseData{}
	for rows.Next() {
		withdrawal := &responses.WithdrawalResponseData{
			Recipient: &models.Recipient{
				Details: &models.RecipientDetails{},
			},
			Wallet: &responses.UserWalletResponseData{},
			User:   &models.Account{},
		}
		err = rows.Scan(
			&withdrawal.ID, &withdrawal.Reference, &withdrawal.TransactionID, &withdrawal.TransactionNote,
			&withdrawal.Narration, &withdrawal.Status, &withdrawal.Recipient.Type, &withdrawal.Recipient.Details.Name,
			&withdrawal.Recipient.Details.DestinationTag, &withdrawal.Recipient.Details.Address,

			&withdrawal.Wallet.ID,
		)
		if err != nil {
			return nil, errors.HandleDataDBError(err)
		}
		withdrawals[withdrawal.TransactionID] = withdrawal
	}

	data, err := w.populateWithdrawals(ctx, withdrawals, user.Data)
	if err != nil {
		return nil, err
	}

	return &responses.Response[[]*responses.WithdrawalResponseData]{
		Data: data,
	}, nil
}

func (w *withdrawalService) populateWithdrawals(ctx context.Context, withdrawals map[string]*responses.WithdrawalResponseData, user *models.Account) ([]*responses.WithdrawalResponseData, error) {
	walletIds := make([]string, 0)
	transferIds := make([]tdb_types.Uint128, 0)

	for txid, withdrawal := range withdrawals {
		walletIds = append(walletIds, withdrawal.Wallet.ID)
		txId, err := tdb_types.HexStringToUint128(txid)
		if err != nil {
			return nil, err
		}
		transferIds = append(transferIds, txId)
	}

	wallets, err := w.walletService.LookupWallets(ctx, walletIds)
	if err != nil {
		return nil, err
	}
	withdrawalTxs, err := w.transactionDB.LookupTransfers(transferIds)
	if err != nil {
		return nil, errors.HandleTxDBError(err)
	}

	data := make([]*responses.WithdrawalResponseData, 0)
	for _, tx := range withdrawalTxs {
		withdrawal := withdrawals[tx.ID.String()]
		wallet := wallets[withdrawal.Wallet.ID]

		// amount := tx.Amount.BigInt()
		withdrawal.Wallet = wallet
		withdrawal.User = wallet.User
		withdrawal.Amount = utils.ApproximateAmount(Ledgers[tx.Ledger], utils.FromAmount(tx.Amount))
		withdrawal.Currency = Ledgers[tx.Ledger]
		withdrawal.Type = withdrawal.Recipient.Type
		withdrawal.Fee = 0
		withdrawal.Total = withdrawal.Amount
		withdrawal.CreatedAt = time.UnixMicro(int64(tx.Timestamp / 1000))
		withdrawal.DoneAt = withdrawal.CreatedAt

		switch {
		case withdrawal.User.ID == user.ID:
		case withdrawal.User.ParentID != nil && *withdrawal.User.ParentID == user.ID:
		case withdrawal.User.ParentID != nil && user.ParentID != nil && *withdrawal.User.ParentID == *user.ParentID:
		default:
			withdrawal.User = &models.Account{
				ID:          withdrawal.User.ID,
				ParentID:    withdrawal.User.ParentID,
				FirstName:   withdrawal.User.FirstName,
				LastName:    withdrawal.User.LastName,
				DisplayName: withdrawal.User.DisplayName,
			}
			withdrawal.Wallet = nil
		}

		data = append(data, withdrawal)
	}

	return data, nil
}
