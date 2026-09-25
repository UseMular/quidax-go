package services

import (
	"context"
	"database/sql"
	"strings"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/google/uuid"
	"github.com/lucsky/cuid"
	tdb "github.com/tigerbeetle/tigerbeetle-go"
	tdb_types "github.com/tigerbeetle/tigerbeetle-go/pkg/types"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	"github.com/2HgO/quidax-go/errors"
	"github.com/2HgO/quidax-go/models"
	"github.com/2HgO/quidax-go/types/requests"
	"github.com/2HgO/quidax-go/types/responses"
)

type AccountService interface {
	CreateSubAccount(context.Context, *requests.CreateSubAccountRequest) (*responses.Response[*models.Account], error)
	EditSubAccountDetails(context.Context, *requests.EditSubAccountDetailsRequest) (*responses.Response[*models.Account], error)
	FetchAllSubAccounts(context.Context, *requests.FetchAllSubAccountsRequest) (*responses.Response[[]*models.Account], error)
	FetchAccountDetails(context.Context, *requests.FetchAccountDetailsRequest) (*responses.Response[*models.Account], error)
	SupportNewToken(context.Context, string) error

	CreateAccount(context.Context, *requests.CreateAccountRequest) (*responses.Response[*responses.CreateAccountResponseData], error)
	UpdateWebHookURL(context.Context, *requests.UpdateWebhookURLRequest) error
	// GenerateToken(context.Context, *requests.GenerateTokenRequest) (*responses.Response[*models.AccessToken], error)
	GetAccountByAccessToken(context.Context, string) (*models.Account, error)
}

func NewAccountService(txDatabase tdb.Client, dataDatabase *sql.DB, log *zap.Logger) AccountService {
	return &accountService{
		service{
			transactionDB: txDatabase,
			dataDB:        dataDatabase,
			log:           log,
		},
	}
}

type accountService struct {
	service
}

func (a *accountService) CreateAccount(ctx context.Context, req *requests.CreateAccountRequest) (*responses.Response[*responses.CreateAccountResponseData], error) {
	now := time.Now()
	accountID := uuid.New()

	account := &models.Account{
		ID:          accountID.String(),
		SN:          cuid.New(),
		DisplayName: req.DisplayName,
		Email:       cases.Lower(language.English).String(req.Email),
		FirstName:   cases.Title(language.English).String(req.FirstName),
		LastName:    cases.Title(language.English).String(req.LastName),
		CreatedAt:   &now,
		UpdatedAt:   &now,
	}

	password, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}

	tx, err := a.dataDB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	// Defer a rollback in case anything fails.
	defer tx.Rollback()

	// * create user account
	_, err = sq.
		Insert("accounts").
		Columns("id", "sn", "display_name", "email", "first_name", "last_name", "created_at", "updated_at", "is_main_account").
		Values(account.ID, account.SN, account.DisplayName, account.Email, account.FirstName, account.LastName, now, now, true).
		RunWith(tx).
		ExecContext(ctx)

	if err != nil {
		return nil, errors.HandleDataDBError(err)
	}

	credentials := &models.Credentials{
		ID:       account.ID,
		Password: string(password),
	}

	// * create user access token to authenticate requests
	_, err = sq.
		Insert("credentials").
		Columns("id", "password").
		Values(credentials.ID, credentials.Password).
		RunWith(tx).
		ExecContext(ctx)

	if err != nil {
		return nil, errors.HandleDataDBError(err)
	}

	accessToken := &models.AccessToken{
		ID:          uuid.NewString(),
		Name:        "Default Token",
		Description: "default token for user requests",
		AccountID:   account.ID,
		Token:       "pub_test_" + cuid.Slug(),
	}

	// * create user access token to authenticate requests
	_, err = sq.
		Insert("access_tokens").
		Columns("id", "name", "description", "account_id", "token").
		Values(accessToken.ID, accessToken.Name, accessToken.Description, accessToken.AccountID, accessToken.Token).
		RunWith(tx).
		ExecContext(ctx)

	if err != nil {
		return nil, errors.HandleDataDBError(err)
	}

	wallets := make([]tdb_types.Account, 0, len(Ledgers))
	for ledgerId := range Ledgers {
		wallets = append(wallets, tdb_types.Account{
			ID: tdb_types.ID(),
			Flags: tdb_types.AccountFlags{
				History:                    true,
				DebitsMustNotExceedCredits: true,
				Linked:                     len(wallets) < (len(Ledgers) - 1),
			}.ToUint16(),
			Ledger:      ledgerId,
			Code:        1,
			UserData128: tdb_types.BytesToUint128(accountID),
		})
	}

	// * store wallets ref in wallets collection
	walletsInsertStmt := sq.
		Insert("wallets").
		Columns("id", "account_id", "token")
	for _, wallet := range wallets {
		walletsInsertStmt = walletsInsertStmt.
			Values(wallet.ID.String(), account.ID, Ledgers[wallet.Ledger])
	}

	_, err = walletsInsertStmt.
		RunWith(tx).
		ExecContext(ctx)
	if err != nil {
		return nil, errors.HandleDataDBError(err)
	}

	// * create wallet accounts in financial transaction database
	txRes, err := a.transactionDB.CreateAccounts(wallets)
	if err != nil {
		return nil, errors.HandleTxDBError(err)
	}
	if len(txRes) > 0 {
		return nil, errors.NewUnknownError(txRes[0].Result.String())
	}

	if err = tx.Commit(); err != nil {
		return nil, errors.HandleDataDBError(err)
	}

	return &responses.Response[*responses.CreateAccountResponseData]{
		Status:  "success",
		Message: "Account Created successfully",
		Data: &responses.CreateAccountResponseData{
			User:  account,
			Token: accessToken,
		},
	}, nil
}

func (a *accountService) FetchAccountDetails(ctx context.Context, req *requests.FetchAccountDetailsRequest) (*responses.Response[*models.Account], error) {
	parent, ok := ctx.Value("user").(*models.Account)
	if ok && req.UserID == parent.ID {
		req.UserID = "me"
	}
	stmt := sq.
		Select("accounts.id", "sn", "display_name", "email", "first_name", "last_name", "created_at", "updated_at", "callback_url", "webhook_key").
		From("accounts").
		LeftJoin("webhook_details on webhook_details.id = accounts.id OR webhook_details.id = accounts.parent_id")

	switch req.UserID {
	case "me":
		parent := ctx.Value("user").(*models.Account)
		stmt = stmt.Where(sq.Eq{"is_main_account": true, "accounts.id": parent.ID})
	default:
		if ctx.Value("skip_check") == nil {
			stmt = stmt.Where(sq.Eq{"accounts.id": req.UserID, "parent_id": parent.ID})
		} else {
			stmt = stmt.Where(sq.Eq{"accounts.id": req.UserID})
		}
	}

	row := stmt.
		Limit(1).
		RunWith(a.dataDB).
		QueryRowContext(ctx)

	if row == nil {
		return nil, errors.NewNotFoundError("user not found")
	}
	var account = &models.Account{}
	err := row.Scan(&account.ID, &account.SN, &account.DisplayName, &account.Email, &account.FirstName, &account.LastName, &account.CreatedAt, &account.UpdatedAt, &account.WebhookDetails.CallbackURL, &account.WebhookDetails.WebhookKey)
	if err != nil {
		return nil, errors.HandleDataDBError(err)
	}

	return &responses.Response[*models.Account]{
		Status:  "success",
		Message: "Successful",
		Data:    account,
	}, nil
}

func (a *accountService) GetAccountByAccessToken(ctx context.Context, token string) (*models.Account, error) {
	row := sq.
		Select("accounts.id", "accounts.email", "webhook_details.callback_url", "accounts.display_name", "webhook_details.webhook_key").
		From("access_tokens").
		Join("accounts on access_tokens.account_id = accounts.id").
		LeftJoin("webhook_details on webhook_details.id = accounts.id").
		Where(sq.Eq{"token": token}).
		RunWith(a.dataDB).
		QueryRowContext(ctx)

	if row == nil {
		return nil, errors.NewNotFoundError("token not found")
	}
	var account = &models.Account{}
	err := row.Scan(&account.ID, &account.Email, &account.WebhookDetails.CallbackURL, &account.DisplayName, &account.WebhookDetails.WebhookKey)
	if err != nil {
		return nil, errors.HandleDataDBError(err)
	}

	return account, nil
}

func (a *accountService) UpdateWebHookURL(ctx context.Context, req *requests.UpdateWebhookURLRequest) error {
	parent := ctx.Value("user").(*models.Account)

	_, err := sq.
		Replace("webhook_details").
		Columns("id", "callback_url", "webhook_key").
		Values(parent.ID, req.CallbackURL, req.WebhookKey).
		RunWith(a.dataDB).
		ExecContext(ctx)
	if err != nil {
		return errors.HandleDataDBError(err)
	}

	return nil
}

func (a *accountService) CreateSubAccount(ctx context.Context, req *requests.CreateSubAccountRequest) (*responses.Response[*models.Account], error) {
	parent := ctx.Value("user").(*models.Account)

	now := time.Now()
	accountID := uuid.New()
	account := &models.Account{
		ID:          accountID.String(),
		SN:          cuid.New(),
		DisplayName: parent.DisplayName,
		Email:       strings.ToLower(req.Email),
		FirstName:   strings.ToTitle(req.FirstName),
		LastName:    strings.ToTitle(req.LastName),
		ParentID:    &parent.ID,
		CreatedAt:   &now,
		UpdatedAt:   &now,
	}

	tx, err := a.dataDB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	// Defer a rollback in case anything fails.
	defer tx.Rollback()

	// * create sub account user
	_, err = sq.
		Insert("accounts").
		Columns("id", "sn", "display_name", "email", "first_name", "last_name", "created_at", "updated_at", "is_main_account", "parent_id").
		Values(account.ID, account.SN, parent.DisplayName, account.Email, account.FirstName, account.LastName, now, now, false, parent.ID).
		RunWith(tx).
		ExecContext(ctx)

	if err != nil {
		return nil, err
	}

	wallets := make([]tdb_types.Account, 0, len(Ledgers))
	for ledgerId := range Ledgers {
		wallets = append(wallets, tdb_types.Account{
			ID: tdb_types.ID(),
			Flags: tdb_types.AccountFlags{
				History:                    true,
				DebitsMustNotExceedCredits: true,
				Linked:                     len(wallets) < (len(Ledgers) - 1),
			}.ToUint16(),
			Ledger:      ledgerId,
			Code:        1,
			UserData128: tdb_types.BytesToUint128(accountID),
		})
	}

	// * store wallets ref in wallets collection
	walletsInsertStmt := sq.
		Insert("wallets").
		Columns("id", "account_id", "token")
	for _, wallet := range wallets {
		walletsInsertStmt = walletsInsertStmt.
			Values(wallet.ID.String(), account.ID, Ledgers[wallet.Ledger])
	}

	_, err = walletsInsertStmt.
		RunWith(tx).
		ExecContext(ctx)
	if err != nil {
		return nil, errors.HandleDataDBError(err)
	}

	// * create wallet accounts in financial transaction database
	txRes, err := a.transactionDB.CreateAccounts(wallets)
	if err != nil {
		return nil, errors.HandleTxDBError(err)
	}
	if len(txRes) > 0 {
		return nil, errors.NewUnknownError(txRes[0].Result.String())
	}

	if err = tx.Commit(); err != nil {
		return nil, errors.HandleDataDBError(err)
	}

	return &responses.Response[*models.Account]{
		Status:  "success",
		Message: "Account Created successfully",
		Data:    account,
	}, nil
}

func (a *accountService) EditSubAccountDetails(ctx context.Context, req *requests.EditSubAccountDetailsRequest) (*responses.Response[*models.Account], error) {
	parent := ctx.Value("user").(*models.Account)
	if req.UserID == parent.ID {
		req.UserID = "me"
	}

	stmt := sq.
		Update("accounts").
		Set("updated_at", time.Now())

	if req.FirstName != "" {
		stmt = stmt.Set("first_name", req.FirstName)
	}
	if req.LastName != "" {
		stmt = stmt.Set("last_name", req.LastName)
	}
	if req.PhoneNumber != "" {
		// todo: add phone number to accounts schema
		// stmt = stmt.Set("first_name", req.FirstName)
	}

	switch req.UserID {
	case "me":
		stmt = stmt.Where(sq.Eq{"is_main_account": true, "id": parent.ID})
	default:
		stmt = stmt.Where(sq.Eq{"id": req.UserID, "parent_id": parent.ID})
	}

	_, err := stmt.RunWith(a.dataDB).ExecContext(ctx)

	if err != nil {
		return nil, errors.HandleDataDBError(err)
	}

	return a.FetchAccountDetails(ctx, &requests.FetchAccountDetailsRequest{UserID: req.UserID})
}

func (a *accountService) FetchAllSubAccounts(ctx context.Context, req *requests.FetchAllSubAccountsRequest) (*responses.Response[[]*models.Account], error) {
	parent := ctx.Value("user").(*models.Account)

	rows, err := sq.
		Select("id", "sn", "display_name", "email", "first_name", "last_name", "created_at", "updated_at").
		From("accounts").
		Where(sq.Eq{"parent_id": parent.ID}).
		RunWith(a.dataDB).
		QueryContext(ctx)
	if err != nil {
		return nil, errors.HandleDataDBError(err)
	}

	res := make([]*models.Account, 0)
	for rows.Next() {
		acc := &models.Account{}
		err := rows.Scan(&acc.ID, &acc.SN, &acc.DisplayName, &acc.Email, &acc.FirstName, &acc.LastName, &acc.CreatedAt, &acc.UpdatedAt)
		if err != nil {
			return nil, errors.HandleDataDBError(err)
		}
		res = append(res, acc)
	}

	return &responses.Response[[]*models.Account]{
		Status: "success",
		Data:   res,
	}, nil
}

func (a *accountService) SupportNewToken(ctx context.Context, token string) error {
	parent := ctx.Value("user").(*models.Account)
	subAccounts, err := a.FetchAllSubAccounts(ctx, nil)
	if err != nil {
		return err
	}
	hasParentAccount := false
	for _, account := range subAccounts.Data {
		if account.ID == parent.ID {
			hasParentAccount = true
			break
		}
	}
	if !hasParentAccount {
		subAccounts.Data = append(subAccounts.Data, parent)
	}
	wallets := make([]tdb_types.Account, 0, len(subAccounts.Data))
	walletsInsertStmt := sq.
		Insert("wallets").
		Columns("id", "account_id", "token")
	ledgerId := LedgerIDs[token]
	for _, subAccount := range subAccounts.Data {
		wallet := tdb_types.Account{
			ID: tdb_types.ID(),
			Flags: tdb_types.AccountFlags{
				History:                    true,
				DebitsMustNotExceedCredits: true,
				Linked:                     false,
			}.ToUint16(),
			Ledger:      ledgerId,
			Code:        1,
			UserData128: tdb_types.BytesToUint128(uuid.MustParse(subAccount.ID)),
		}
		wallets = append(wallets, wallet)
		walletsInsertStmt = walletsInsertStmt.
			Values(wallet.ID.String(), subAccount.ID, Ledgers[wallet.Ledger])
	}

	tx, err := a.dataDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	// Defer a rollback in case anything fails.
	defer tx.Rollback()

	_, err = walletsInsertStmt.
		RunWith(tx).
		ExecContext(ctx)
	if err != nil {
		return errors.HandleDataDBError(err)
	}

	txRes, err := a.transactionDB.CreateAccounts(wallets)
	if err != nil {
		return errors.HandleTxDBError(err)
	}
	if len(txRes) > 0 {
		return errors.NewUnknownError(txRes[0].Result.String())
	}

	if err = tx.Commit(); err != nil {
		return errors.HandleDataDBError(err)
	}

	return nil
}
