package handlers

import (
	"net/http"

	"github.com/2HgO/quidax-go/errors"
	"github.com/2HgO/quidax-go/services"
	"github.com/2HgO/quidax-go/types/requests"
	"github.com/2HgO/quidax-go/utils"
	"go.uber.org/zap"
)

type AccountHandler interface {
	CreateAccount(http.ResponseWriter, *http.Request)
	UpdateWebHookURL(http.ResponseWriter, *http.Request)

	FetchAccountDetails(http.ResponseWriter, *http.Request)
	CreateSubAccount(http.ResponseWriter, *http.Request)
	EditSubAccountDetails(http.ResponseWriter, *http.Request)
	FetchAllSubAccounts(http.ResponseWriter, *http.Request)

	Handler
}

func NewAccountHandler(accountService services.AccountService, middlewares MiddleWareHandler, log *zap.Logger) AccountHandler {
	return &accountHandler{
		handler: handler{accountService: accountService, middlewares: middlewares, log: log},
	}
}

type accountHandler struct {
	handler
}

func (a *accountHandler) ServeHttp(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/accounts", a.CreateAccount)

	mux.HandleFunc("PUT /api/v1/accounts", a.middlewares.AttachValidateAccessToken(a.UpdateWebHookURL))
	mux.HandleFunc("POST /api/v1/accounts/new-token", a.middlewares.AttachValidateAccessToken(a.SupportNewToken))

	mux.HandleFunc("POST /api/v1/users", a.middlewares.AttachValidateAccessToken(a.CreateSubAccount))
	mux.HandleFunc("GET /api/v1/users", a.middlewares.AttachValidateAccessToken(a.FetchAllSubAccounts))
	mux.HandleFunc("PUT /api/v1/users/{user_id}", a.middlewares.AttachValidateAccessToken(a.EditSubAccountDetails))
	mux.HandleFunc("GET /api/v1/users/{user_id}", a.middlewares.AttachValidateAccessToken(a.FetchAccountDetails))
}

func (a *accountHandler) CreateAccount(w http.ResponseWriter, r *http.Request) {
	req := utils.Bind[requests.CreateAccountRequest](r)

	res, err := a.accountService.CreateAccount(r.Context(), req)
	if err != nil {
		errors.AsAppError(err).Serialize(w)
		return
	}

	utils.JSON(w, 201, res)
}

func (a *accountHandler) UpdateWebHookURL(w http.ResponseWriter, r *http.Request) {
	req := utils.Bind[requests.UpdateWebhookURLRequest](r)

	err := a.accountService.UpdateWebHookURL(r.Context(), req)
	if err != nil {
		errors.AsAppError(err).Serialize(w)
		return
	}

	w.WriteHeader(204)
	w.Write(nil)
}

func (a *accountHandler) FetchAccountDetails(w http.ResponseWriter, r *http.Request) {
	req := utils.Bind[requests.FetchAccountDetailsRequest](r)

	res, err := a.accountService.FetchAccountDetails(r.Context(), req)
	if err != nil {
		errors.AsAppError(err).Serialize(w)
		return
	}

	utils.JSON(w, 200, res)
}

func (a *accountHandler) CreateSubAccount(w http.ResponseWriter, r *http.Request) {
	req := utils.Bind[requests.CreateSubAccountRequest](r)

	res, err := a.accountService.CreateSubAccount(r.Context(), req)
	if err != nil {
		errors.AsAppError(err).Serialize(w)
		return
	}

	utils.JSON(w, 201, res)
}

func (a *accountHandler) EditSubAccountDetails(w http.ResponseWriter, r *http.Request) {
	req := utils.Bind[requests.EditSubAccountDetailsRequest](r)

	res, err := a.accountService.EditSubAccountDetails(r.Context(), req)
	if err != nil {
		errors.AsAppError(err).Serialize(w)
		return
	}

	utils.JSON(w, 200, res)
}

func (a *accountHandler) FetchAllSubAccounts(w http.ResponseWriter, r *http.Request) {
	req := utils.Bind[requests.FetchAllSubAccountsRequest](r)

	res, err := a.accountService.FetchAllSubAccounts(r.Context(), req)
	if err != nil {
		errors.AsAppError(err).Serialize(w)
		return
	}

	utils.JSON(w, 200, res)
}

func (a *accountHandler) SupportNewToken(w http.ResponseWriter, r *http.Request) {
	req := utils.Bind[struct {Currency string `json:"currency"`}](r)

	if err := a.accountService.SupportNewToken(r.Context(), req.Currency); err != nil {
		errors.AsAppError(err).Serialize(w)
		return
	}

	utils.JSON(w, 204, "")
}
