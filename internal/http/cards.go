package httpapi

import (
	"fcard/internal/card"
	"fcard/internal/domain"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func (api *API) requireCards() gin.HandlerFunc {
	return func(request *gin.Context) {
		if api.cards == nil {
			api.handleError(request, domain.ErrIssuingDisabled)
			return
		}
		request.Next()
	}
}
func (api *API) cardID(request *gin.Context, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(request.Param(name))
	if err != nil || id == uuid.Nil {
		api.handleError(request, domain.ErrInvalid)
		return uuid.Nil, false
	}
	return id, true
}
func (api *API) getCardholder(request *gin.Context) {
	result, err := api.cards.Binding(request.Request.Context())
	if err != nil {
		api.handleError(request, err)
		return
	}
	api.success(request, 200, result)
}
func (api *API) bindCardholder(request *gin.Context) {
	input, ok := decode[struct {
		CardholderID string `json:"provider_cardholder_id"`
	}](api, request)
	if !ok {
		return
	}
	result, err := api.cards.Bind(request.Request.Context(), principal(request), input.CardholderID, request.GetString("request_id"))
	if err != nil {
		api.handleError(request, err)
		return
	}
	api.success(request, 200, result)
}
func (api *API) cardAvailability(request *gin.Context) {
	result, err := api.cards.Availability(request.Request.Context(), userPrincipal(request))
	if err != nil {
		api.handleError(request, err)
		return
	}
	api.success(request, 200, result)
}
func (api *API) createUserCard(request *gin.Context) {
	input, ok := decode[card.CreateInput](api, request)
	if !ok {
		return
	}
	result, err := api.cards.Create(request.Request.Context(), userPrincipal(request), request.GetHeader("Idempotency-Key"), request.GetString("request_id"), input)
	if err != nil {
		api.handleError(request, err)
		return
	}
	api.success(request, 202, result)
}
func (api *API) listUserCards(request *gin.Context) {
	page, size, err := pagination(request)
	if err != nil {
		api.handleError(request, err)
		return
	}
	result, err := api.cards.List(request.Request.Context(), userPrincipal(request).User.ID, page, size)
	if err != nil {
		api.handleError(request, err)
		return
	}
	api.success(request, 200, result)
}
func (api *API) getUserCard(request *gin.Context) {
	id, ok := api.cardID(request, "id")
	if !ok {
		return
	}
	result, err := api.cards.Get(request.Request.Context(), userPrincipal(request).User.ID, id)
	if err != nil {
		api.handleError(request, err)
		return
	}
	api.success(request, 200, result)
}
func (api *API) syncUserCard(request *gin.Context) {
	id, ok := api.cardID(request, "id")
	if !ok {
		return
	}
	result, err := api.cards.Sync(request.Request.Context(), userPrincipal(request).User.ID, id)
	if err != nil {
		api.handleError(request, err)
		return
	}
	api.success(request, 200, result)
}
func (api *API) getUserCardOperation(request *gin.Context) {
	id, ok := api.cardID(request, "id")
	if !ok {
		return
	}
	result, err := api.cards.Operation(request.Request.Context(), userPrincipal(request).User.ID, id)
	if err != nil {
		api.handleError(request, err)
		return
	}
	api.success(request, 200, result)
}
func (api *API) retryUserCardOperation(request *gin.Context) {
	id, ok := api.cardID(request, "id")
	if !ok {
		return
	}
	result, err := api.cards.Retry(request.Request.Context(), userPrincipal(request), id, request.GetString("request_id"))
	if err != nil {
		api.handleError(request, err)
		return
	}
	api.success(request, 202, result)
}
func (api *API) adminCardOperation(request *gin.Context) {
	userID, ok := api.cardID(request, "id")
	if !ok {
		return
	}
	operationID, ok := api.cardID(request, "operationID")
	if !ok {
		return
	}
	result, err := api.cards.Operation(request.Request.Context(), userID, operationID)
	if err != nil {
		api.handleError(request, err)
		return
	}
	api.success(request, 200, result)
}
func (api *API) reconcileCardOperation(request *gin.Context) {
	userID, ok := api.cardID(request, "id")
	if !ok {
		return
	}
	operationID, ok := api.cardID(request, "operationID")
	if !ok {
		return
	}
	input, ok := decode[struct {
		CardID string `json:"provider_card_id"`
	}](api, request)
	if !ok {
		return
	}
	result, err := api.cards.Reconcile(request.Request.Context(), principal(request), userID, operationID, input.CardID, request.GetString("request_id"))
	if err != nil {
		api.handleError(request, err)
		return
	}
	api.success(request, 200, result)
}
