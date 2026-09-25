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
	"github.com/google/uuid"
	tdb "github.com/tigerbeetle/tigerbeetle-go"
	tdb_types "github.com/tigerbeetle/tigerbeetle-go/pkg/types"
	"go.uber.org/zap"
)

type DepositService interface {
	CreateDeposit(context.Context, *requests.DepositAmountRequest) (*responses.Response[*responses.DepositResponseData], error)

	FetchDeposit(context.Context, *requests.FetchDepositRequest) (*responses.Response[*responses.DepositResponseData], error)
	FetchDeposits(context.Context, *requests.FetchDepositsRequest) (*responses.Response[[]*responses.DepositResponseData], error)
}

func NewDepositService(
	accountService AccountService,
	walletService WalletService,
	webhookService WebhookService,
	txDatabase tdb.Client,
	dataDatabase *sql.DB,
	log *zap.Logger,
) DepositService {
	return &depositService{
		service{
			accountService: accountService,
			transactionDB:  txDatabase,
			dataDB:         dataDatabase,
			log:            log,
			walletService:  walletService,
			webhookService: webhookService,
		},
	}
}

type depositService struct {
	service
}

func (d *depositService) CreateDeposit(ctx context.Context, req *requests.DepositAmountRequest) (*responses.Response[*responses.DepositResponseData], error) {
	wallet, err := d.walletService.FetchUserWallet(ctx, &requests.FetchUserWalletRequest{UserID: req.UserID, Currency: req.Currency})
	if err != nil {
		return nil, err
	}
	walletId, _ := tdb_types.HexStringToUint128(wallet.Data.ID)

	amount := utils.ApproximateAmount(wallet.Data.Currency, float64(req.Amount))
	transfer := tdb_types.Transfer{
		ID:              tdb_types.ID(),
		Amount:          utils.ToAmount(amount),
		CreditAccountID: walletId,
		DebitAccountID:  tdb_types.ToUint128(uint64(LedgerIDs[wallet.Data.Currency])),
		Ledger:          LedgerIDs[wallet.Data.Currency],
		Code:            3,
	}

	res, err := d.transactionDB.CreateTransfers([]tdb_types.Transfer{transfer})
	if err != nil {
		return nil, err
	}
	if len(res) > 0 {
		return nil, errors.NewUnknownError(res[0].Result.String())
	}

	data, err := d.FetchDeposit(ctx, &requests.FetchDepositRequest{UserID: req.UserID, TransactionID: transfer.ID.String()})
	if err != nil {
		return nil, err
	}

	go d.webhookService.SendDepositSuccessfulEvent(wallet.Data.User.WebhookDetails, data.Data)
	return data, nil
}

func (d *depositService) FetchDeposit(ctx context.Context, req *requests.FetchDepositRequest) (*responses.Response[*responses.DepositResponseData], error) {
	user, err := d.accountService.FetchAccountDetails(ctx, &requests.FetchAccountDetailsRequest{UserID: req.UserID})
	if err != nil {
		return nil, err
	}

	txid, err := tdb_types.HexStringToUint128(req.TransactionID)
	if err != nil {
		return nil, err
	}

	transfer, err := d.transactionDB.LookupTransfers([]tdb_types.Uint128{txid})
	if err != nil {
		return nil, errors.HandleTxDBError(err)
	}
	if len(transfer) != 1 {
		return nil, errors.NewNotFoundError("deposit not found")
	}

	deposit := transfer[0]
	wallets, err := d.walletService.LookupWallets(ctx, []string{deposit.CreditAccountID.String()})
	if err != nil {
		return nil, err
	}

	wallet := wallets[deposit.CreditAccountID.String()]
	if len(wallets) != 1 || wallet.User.ID != user.Data.ID {
		return nil, errors.NewNotFoundError("deposit not found")
	}

	data := &responses.DepositResponseData{
		ID:        deposit.ID.String(),
		Type:      models.CoinAddress_RecipientType,
		User:      user.Data,
		Wallet:    wallet,
		Currency:  wallet.Currency,
		Amount:    utils.FromAmount(deposit.Amount),
		CreatedAt: time.UnixMicro(int64(deposit.Timestamp / 1000)),
		DoneAt:    time.UnixMicro(int64(deposit.Timestamp / 1000)),
		Fee:       0,
		Status:    "completed",
		TxID:      deposit.ID.String(),
	}

	return &responses.Response[*responses.DepositResponseData]{
		Status:  "success",
		Message: "Successful",
		Data:    data,
	}, nil
}

func (d *depositService) FetchDeposits(ctx context.Context, req *requests.FetchDepositsRequest) (*responses.Response[[]*responses.DepositResponseData], error) {
	user, err := d.accountService.FetchAccountDetails(ctx, &requests.FetchAccountDetailsRequest{UserID: req.UserID})
	if err != nil {
		return nil, err
	}

	query := tdb_types.AccountFilter{
		UserData128: tdb_types.BytesToUint128(uuid.MustParse(user.Data.ID)),
		Code:        3,
		Limit:       8000,
		Flags: tdb_types.AccountFilterFlags{
			Reversed: true,
			Credits:  true,
		}.ToUint32(),
	}
	if req.Currency != "" {
		wallet, err := d.walletService.FetchUserWallet(ctx, &requests.FetchUserWalletRequest{UserID: req.UserID, Currency: req.Currency})
		if err != nil {
			return nil, err
		}
		query.AccountID, _ = tdb_types.HexStringToUint128(wallet.Data.ID)
		query.UserData128 = tdb_types.ToUint128(0)
	}

	transfers, err := d.transactionDB.GetAccountTransfers(query)
	if err != nil {
		return nil, errors.HandleTxDBError(err)
	}

	walletIds := make([]string, 0)
	for _, transfer := range transfers {
		walletIds = append(walletIds, transfer.CreditAccountID.String())
	}

	wallets, err := d.walletService.LookupWallets(ctx, walletIds)
	if err != nil {
		return nil, err
	}

	data := []*responses.DepositResponseData{}
	for _, transfer := range transfers {
		wallet := wallets[transfer.CreditAccountID.String()]

		deposit := &responses.DepositResponseData{
			ID:        transfer.ID.String(),
			Type:      models.CoinAddress_RecipientType,
			User:      user.Data,
			Wallet:    wallet,
			Currency:  wallet.Currency,
			Amount:    utils.FromAmount(transfer.Amount),
			CreatedAt: time.UnixMicro(int64(transfer.Timestamp / 1000)),
			DoneAt:    time.UnixMicro(int64(transfer.Timestamp / 1000)),
			Fee:       0,
			Status:    "completed",
			TxID:      transfer.ID.String(),
		}

		data = append(data, deposit)
	}

	return &responses.Response[[]*responses.DepositResponseData]{
		Status:  "success",
		Message: "Successful",
		Data:    data,
	}, nil
}
