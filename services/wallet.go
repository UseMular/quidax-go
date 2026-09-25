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
	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	tdb "github.com/tigerbeetle/tigerbeetle-go"
	tdb_types "github.com/tigerbeetle/tigerbeetle-go/pkg/types"
	"go.uber.org/zap"
)

type WalletService interface {
	FetchUserWallets(context.Context, *requests.FetchUserWalletsRequest) (*responses.Response[[]*responses.UserWalletResponseData], error)
	FetchUserWallet(context.Context, *requests.FetchUserWalletRequest) (*responses.Response[*responses.UserWalletResponseData], error)

	LookupWallets(context.Context, []string) (map[string]*responses.UserWalletResponseData, error)
}

func NewWalletService(txDatabase tdb.Client, dataDatabase *sql.DB, accountService AccountService, webhookService WebhookService, log *zap.Logger) WalletService {
	w := &walletService{
		service{
			transactionDB:  txDatabase,
			dataDB:         dataDatabase,
			accountService: accountService,
			webhookService: webhookService,
			log:            log,
		},
	}

	if err := w.initSystemAccounts(); err != nil {
		panic(err)
	}

	return w
}

type walletService struct {
	service
}

func (w *walletService) initSystemAccounts() error {
	systemAccounts := []tdb_types.Account{}
	for accountID := range Ledgers {
		systemAccounts = append(systemAccounts, tdb_types.Account{
			ID:     tdb_types.ToUint128(uint64(accountID)),
			Ledger: accountID,
			Code:   2,
			Flags:  tdb_types.AccountFlags{History: true}.ToUint16(),
		})
	}

	res, err := w.transactionDB.CreateAccounts(systemAccounts)
	if err != nil {
		return err
	}
	for _, r := range res {
		switch r.Result {
		case tdb_types.AccountExists,
			tdb_types.AccountExistsWithDifferentFlags,
			tdb_types.AccountExistsWithDifferentUserData128,
			tdb_types.AccountExistsWithDifferentUserData64,
			tdb_types.AccountExistsWithDifferentUserData32,
			tdb_types.AccountExistsWithDifferentLedger,
			tdb_types.AccountExistsWithDifferentCode:
		default:
			return errors.NewFailedDependencyError(r.Result.String())
		}
	}

	return nil
}

func (w *walletService) FetchUserWallets(ctx context.Context, req *requests.FetchUserWalletsRequest) (*responses.Response[[]*responses.UserWalletResponseData], error) {
	user, err := w.accountService.FetchAccountDetails(ctx, &requests.FetchAccountDetailsRequest{UserID: req.UserID})
	if err != nil {
		return nil, err
	}

	rows, err := sq.
		Select("id", "account_id", "token").
		From("wallets").
		Where(sq.Eq{"account_id": user.Data.ID}).
		RunWith(w.dataDB).
		QueryContext(ctx)
	if err != nil {
		return nil, errors.HandleDataDBError(err)
	}

	var walletsMap = map[string]*models.Wallet{}
	for rows.Next() {
		wallet := &models.Wallet{}
		err = rows.Scan(&wallet.ID, &wallet.AccountID, &wallet.Token)
		if err != nil {
			return nil, errors.HandleDataDBError(err)
		}
		walletsMap[wallet.ID] = wallet
	}

	res, err := w.transactionDB.QueryAccounts(tdb_types.QueryFilter{UserData128: tdb_types.BytesToUint128(uuid.MustParse(user.Data.ID)), Limit: uint32(len(Ledgers))})
	if err != nil {
		return nil, err
	}

	data := make([]*responses.UserWalletResponseData, len(res))
	for i := range res {
		credits := res[i].CreditsPosted.BigInt()
		debits := res[i].DebitsPosted.BigInt()
		pendingDebits := res[i].DebitsPending.BigInt()
		balance := credits.Sub(&credits, &debits)
		balance = balance.Sub(balance, &pendingDebits)
		wallet := walletsMap[res[i].ID.String()]
		data[i] = &responses.UserWalletResponseData{
			ID:                wallet.ID,
			Name:              cases.Upper(language.English).String(wallet.Token),
			Currency:          wallet.Token,
			Balance:           utils.ApproximateAmount(wallet.Token, utils.FromAmount(tdb_types.BigIntToUint128(*balance))),
			LockedBalance:     utils.ApproximateAmount(wallet.Token, utils.FromAmount(tdb_types.BigIntToUint128(pendingDebits))),
			User:              user.Data,
			ConvertedBalance:  utils.ApproximateAmount(wallet.Token, utils.FromAmount(tdb_types.BigIntToUint128(*balance))) * Rates[wallet.Token]["ngn"],
			CreatedAt:         time.UnixMicro(int64(res[i].Timestamp / 1000)),
			UpdatedAt:         time.UnixMicro(int64(res[i].Timestamp / 1000)),
			ReferenceCurrency: "ngn",
			Networks: []any{map[string]any{
				"id":                "bep20",
				"name":              "Binance Smart Chain",
				"deposits_enabled":  false,
				"withdraws_enabled": false,
			}},
			DepositAddress: utils.String("0x34r21r3f4gr1r3rf31r2r"),
			IsCrypto:       wallet.Token != "ngn",
		}
	}

	return &responses.Response[[]*responses.UserWalletResponseData]{
		Status:  "success",
		Message: "Successful",
		Data:    data,
	}, nil
}

func (w *walletService) FetchUserWallet(ctx context.Context, req *requests.FetchUserWalletRequest) (*responses.Response[*responses.UserWalletResponseData], error) {
	user, err := w.accountService.FetchAccountDetails(ctx, &requests.FetchAccountDetailsRequest{UserID: req.UserID})
	if err != nil {
		return nil, err
	}

	row := sq.
		Select("id", "account_id", "token").
		From("wallets").
		Where(sq.Eq{"account_id": user.Data.ID, "token": req.Currency}).
		RunWith(w.dataDB).
		QueryRowContext(ctx)
	if row == nil {
		return nil, errors.NewNotFoundError("wallet not found")
	}

	wallet := &models.Wallet{}
	err = row.Scan(&wallet.ID, &wallet.AccountID, &wallet.Token)
	if err != nil {
		return nil, errors.HandleDataDBError(err)
	}

	walletId, err := tdb_types.HexStringToUint128(wallet.ID)
	if err != nil {
		return nil, err
	}
	res, err := w.transactionDB.LookupAccounts([]tdb_types.Uint128{walletId})
	if err != nil {
		return nil, err
	}
	if len(res) < 1 {
		return nil, errors.NewNotFoundError("wallet not found")
	}

	credits := res[0].CreditsPosted.BigInt()
	debits := res[0].DebitsPosted.BigInt()
	pendingDebits := res[0].DebitsPending.BigInt()
	balance := credits.Sub(&credits, &debits)
	balance = balance.Sub(balance, &pendingDebits)
	data := &responses.UserWalletResponseData{
		ID:               wallet.ID,
		Name:             cases.Upper(language.English).String(wallet.Token),
		Currency:         wallet.Token,
		Balance:          utils.ApproximateAmount(wallet.Token, utils.FromAmount(tdb_types.BigIntToUint128(*balance))),
		LockedBalance:    utils.ApproximateAmount(wallet.Token, utils.FromAmount(tdb_types.BigIntToUint128(pendingDebits))),
		User:             user.Data,
		ConvertedBalance: utils.ApproximateAmount(wallet.Token, utils.FromAmount(tdb_types.BigIntToUint128(*balance))) * Rates[wallet.Token]["ngn"],
		CreatedAt:        time.UnixMicro(int64(res[0].Timestamp / 1000)),
		UpdatedAt:        time.UnixMicro(int64(res[0].Timestamp / 1000)),
		Networks: []any{map[string]any{
			"id":                "bep20",
			"name":              "Binance Smart Chain",
			"deposits_enabled":  false,
			"withdraws_enabled": false,
		}},
		DepositAddress:    utils.String("0x34r21r3f4gr1r3rf31r2r"),
		ReferenceCurrency: "ngn",
		IsCrypto:          wallet.Token == "ngn",
	}

	return &responses.Response[*responses.UserWalletResponseData]{
		Status:  "success",
		Message: "Successful",
		Data:    data,
	}, nil
}

func (w *walletService) LookupWallets(ctx context.Context, ids []string) (map[string]*responses.UserWalletResponseData, error) {
	rows, err := sq.
		Select(
			"wallets.id", "wallets.account_id", "wallets.token",

			"accounts.id", "accounts.sn", "accounts.display_name", "accounts.email", "accounts.first_name",
			"accounts.last_name", "accounts.created_at", "accounts.updated_at",
		).
		From("wallets").
		Join("accounts on wallets.account_id = accounts.id").
		Where(sq.Eq{"wallets.id": ids}).
		RunWith(w.dataDB).
		QueryContext(ctx)
	if err != nil {
		return nil, errors.HandleDataDBError(err)
	}

	var walletsMap = map[string]*models.Wallet{}
	var accountMap = map[string]*models.Account{}
	walletIds := make([]tdb_types.Uint128, 0)
	for rows.Next() {
		wallet := &models.Wallet{}
		account := &models.Account{}
		err = rows.Scan(
			&wallet.ID, &wallet.AccountID, &wallet.Token,

			&account.ID, &account.SN, &account.DisplayName, &account.Email, &account.FirstName,
			&account.LastName, &account.CreatedAt, &account.UpdatedAt,
		)
		if err != nil {
			return nil, errors.HandleDataDBError(err)
		}
		walletId, err := tdb_types.HexStringToUint128(wallet.ID)
		if err != nil {
			return nil, err
		}
		walletIds = append(walletIds, walletId)
		walletsMap[wallet.ID] = wallet
		accountMap[account.ID] = account
	}

	res, err := w.transactionDB.LookupAccounts(walletIds)
	if err != nil {
		return nil, err
	}

	data := make(map[string]*responses.UserWalletResponseData)
	for i := range res {
		credits := res[i].CreditsPosted.BigInt()
		debits := res[i].DebitsPosted.BigInt()
		pendingDebits := res[i].DebitsPending.BigInt()
		balance := credits.Sub(&credits, &debits)
		balance = balance.Sub(balance, &pendingDebits)
		wallet := walletsMap[res[i].ID.String()]
		user := accountMap[wallet.AccountID]

		data[res[i].ID.String()] = &responses.UserWalletResponseData{
			ID:               wallet.ID,
			Name:             cases.Upper(language.English).String(wallet.Token),
			Currency:         wallet.Token,
			Balance:          utils.ApproximateAmount(wallet.Token, utils.FromAmount(tdb_types.BigIntToUint128(*balance))),
			LockedBalance:    utils.ApproximateAmount(wallet.Token, utils.FromAmount(tdb_types.BigIntToUint128(pendingDebits))),
			User:             user,
			ConvertedBalance: utils.ApproximateAmount(wallet.Token, utils.FromAmount(tdb_types.BigIntToUint128(*balance))) * Rates[wallet.Token]["ngn"],
			CreatedAt:        time.UnixMicro(int64(res[i].Timestamp / 1000)),
			UpdatedAt:        time.UnixMicro(int64(res[i].Timestamp / 1000)),
			Networks: []any{map[string]any{
				"id":                "bep20",
				"name":              "Binance Smart Chain",
				"deposits_enabled":  false,
				"withdraws_enabled": false,
			}},
			DepositAddress:    utils.String("0x34r21r3f4gr1r3rf31r2r"),
			ReferenceCurrency: "ngn",
			IsCrypto:          wallet.Token != "ngn",
		}
	}

	return data, nil
}
