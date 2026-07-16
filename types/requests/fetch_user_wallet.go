package requests

type FetchUserWalletRequest struct {
	UserID   string `uri:"user_id" validate:"required"`
	Currency string `uri:"currency" validate:"required,oneof=ngn usdt usdc eth bnb sol btc trx"`
}
