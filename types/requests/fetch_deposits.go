package requests

type FetchDepositsRequest struct {
	UserID   string `uri:"user_id"`
	Currency string `uri:"currency" validate:"omitempty,oneof=ngn usdt usdc eth bnb sol btc trx"`
}
