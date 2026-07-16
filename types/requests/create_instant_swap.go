package requests

import "github.com/2HgO/quidax-go/models"

type CreateInstantSwapRequest struct {
	UserID       string        `uri:"user_id" validate:"required"`
	FromCurrency string        `json:"from_currency" validate:"required,oneof=ngn usdt usdc eth bnb sol btc trx"`
	ToCurrency   string        `json:"to_currency" validate:"required,oneof=ngn usdt usdc eth bnb sol btc trx"`
	FromAmount   models.Double `json:"from_amount" validate:"required,gt=0"`
}
