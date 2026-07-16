package requests

import "github.com/2HgO/quidax-go/models"

type CreateWithdrawalRequest struct {
	UserID          string        `uri:"user_id" validate:"required"`
	FundUid         string        `json:"fund_uid" validate:"required"`
	Currency        string        `json:"currency" validate:"required,oneof=ngn usdt usdc eth bnb sol btc trx"`
	Amount          models.Double `json:"amount" validate:"required,gt=0"`
	TransactionNote string        `json:"transaction_note"`
	Narration       string        `json:"narration"`
}
