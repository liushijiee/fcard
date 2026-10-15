package httpapi

import (
	"fcard/internal/deposit"

	"github.com/gin-gonic/gin"
)

func (api *API) hasDeposits(request *gin.Context) bool {
	if api.deposits == nil {
		api.handleError(request, deposit.ErrDepositsDisabled)
		return false
	}
	return true
}

func (api *API) depositAvailability(request *gin.Context) {
	maximum := "1.000000"
	if api.deposits != nil {
		maximum = api.deposits.MaxOrderAmount()
	}
	api.success(request, 200, gin.H{"available": api.deposits != nil, "ready": api.deposits != nil && api.deposits.Ready(), "enabled": api.deposits != nil && api.deposits.Enabled(), "environment": "nile", "network": "tron", "currency": "USDT", "decimals": 6, "balance_crediting": false, "max_amount": maximum})
}

func (api *API) createDeposit(request *gin.Context) {
	if !api.hasDeposits(request) {
		return
	}
	input, ok := decode[struct {
		Amount string `json:"amount"`
	}](api, request)
	if !ok {
		return
	}
	result, err := api.deposits.Prepare(request.Request.Context(), userPrincipal(request), request.GetHeader("Idempotency-Key"), input.Amount)
	if err != nil {
		api.handleError(request, err)
		return
	}
	api.success(request, 202, result)
}

func (api *API) listDeposits(request *gin.Context) {
	if !api.hasDeposits(request) {
		return
	}
	page, size, err := pagination(request)
	if err != nil {
		api.handleError(request, err)
		return
	}
	result, err := api.deposits.List(request.Request.Context(), userPrincipal(request), page, size)
	if err != nil {
		api.handleError(request, err)
		return
	}
	api.success(request, 200, result)
}

func (api *API) getDeposit(request *gin.Context) {
	if !api.hasDeposits(request) {
		return
	}
	id, ok := api.cardID(request, "id")
	if !ok {
		return
	}
	result, err := api.deposits.Get(request.Request.Context(), userPrincipal(request), id)
	if err != nil {
		api.handleError(request, err)
		return
	}
	api.success(request, 200, result)
}

func (api *API) managedDeposits(request *gin.Context) {
	if !api.hasDeposits(request) {
		return
	}
	page, size, err := pagination(request)
	if err != nil {
		api.handleError(request, err)
		return
	}
	result, err := api.deposits.Management(request.Request.Context(), principal(request), page, size)
	if err != nil {
		api.handleError(request, err)
		return
	}
	api.success(request, 200, result)
}

func (api *API) managedDeposit(request *gin.Context) {
	if !api.hasDeposits(request) {
		return
	}
	id, ok := api.cardID(request, "id")
	if !ok {
		return
	}
	result, err := api.deposits.ManagedOrder(request.Request.Context(), principal(request), id)
	if err != nil {
		api.handleError(request, err)
		return
	}
	api.success(request, 200, result)
}

func (api *API) recoverDepositQuote(request *gin.Context) {
	if !api.hasDeposits(request) {
		return
	}
	id, ok := api.cardID(request, "id")
	if !ok {
		return
	}
	input, ok := decode[deposit.RecoveryInput](api, request)
	if !ok {
		return
	}
	result, err := api.deposits.RecoverQuote(request.Request.Context(), principal(request), id, input, request.GetString("request_id"))
	if err != nil {
		api.handleError(request, err)
		return
	}
	api.success(request, 200, result)
}

func (api *API) requeueDepositJob(request *gin.Context) {
	if !api.hasDeposits(request) {
		return
	}
	id, ok := api.cardID(request, "id")
	if !ok {
		return
	}
	input, ok := decode[struct {
		Reason string `json:"reason"`
	}](api, request)
	if !ok {
		return
	}
	if err := api.deposits.Requeue(request.Request.Context(), principal(request), id, input.Reason, request.GetString("request_id")); err != nil {
		api.handleError(request, err)
		return
	}
	api.success(request, 200, gin.H{"requeued": true})
}
