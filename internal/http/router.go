package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"fcard/internal/auth"
	"fcard/internal/card"
	"fcard/internal/deposit"
	"fcard/internal/domain"
	"fcard/internal/epusdt"
	"fcard/internal/user"
	"fcard/internal/userauth"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type API struct {
	auth     *auth.Service
	users    *user.Service
	ready    func(context.Context) error
	logger   *slog.Logger
	userAuth *userauth.Service
	cards    *card.Service
	deposits *deposit.Service
}

type response struct {
	Data      any       `json:"data,omitempty"`
	Error     *apiError `json:"error,omitempty"`
	RequestID string    `json:"request_id"`
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func New(authService *auth.Service, users *user.Service, ready func(context.Context) error, logger *slog.Logger, userAuth *userauth.Service, cardServices ...*card.Service) *gin.Engine {
	return NewWithDeposits(authService, users, ready, logger, userAuth, nil, nil, cardServices...)
}

func NewWithDeposits(authService *auth.Service, users *user.Service, ready func(context.Context) error, logger *slog.Logger, userAuth *userauth.Service, deposits *deposit.Service, callback http.Handler, cardServices ...*card.Service) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.SetTrustedProxies(nil)
	api := &API{auth: authService, users: users, ready: ready, logger: logger, userAuth: userAuth, deposits: deposits}
	if len(cardServices) > 0 {
		api.cards = cardServices[0]
	}
	router.Use(api.requestContext(), api.recover())
	router.GET("/health/live", func(request *gin.Context) {
		api.success(request, http.StatusOK, gin.H{"status": "ok"})
	})
	router.GET("/health/ready", func(request *gin.Context) {
		if err := api.ready(request.Request.Context()); err != nil {
			api.fail(request, http.StatusServiceUnavailable, "not_ready", "database or migrations not ready")
			return
		}
		api.success(request, http.StatusOK, gin.H{"status": "ready"})
	})
	router.POST("/api/v1/admin/auth/login", api.login)
	// Public operational flags only; no wallet, merchant or order data.
	router.GET("/health/deposits", api.depositAvailability)
	admin := router.Group("/api/v1/admin", api.authenticate())
	admin.GET("/auth/me", api.me)
	admin.POST("/auth/logout", api.logout)
	admin.POST("/users", api.createUser)
	admin.GET("/users", api.listUsers)
	admin.GET("/users/:id", api.getUser)
	admin.PATCH("/users/:id", api.updateUser)
	admin.GET("/audit-logs", api.listAudit)
	admin.GET("/deposits", api.managedDeposits)
	admin.GET("/deposits/:id", api.managedDeposit)
	admin.POST("/deposits/:id/recover-quote", api.recoverDepositQuote)
	admin.POST("/deposit-jobs/:id/requeue", api.requeueDepositJob)
	admin.POST("/users/:id/password/reset", api.resetUserPassword)
	adminCards := admin.Group("", api.requireCards())
	adminCards.GET("/company/cardholder", api.getCardholder)
	adminCards.POST("/company/cardholder", api.bindCardholder)
	adminCards.GET("/users/:id/card-operations/:operationID", api.adminCardOperation)
	adminCards.POST("/users/:id/card-operations/:operationID/reconcile", api.reconcileCardOperation)
	router.POST("/api/v1/user/auth/login", api.userLogin)
	portal := router.Group("/api/v1/user", api.requireUserSession())
	portal.GET("/auth/me", api.userMe)
	portal.POST("/auth/logout", api.userLogout)
	portal.POST("/auth/change-password", api.changeUserPassword)
	userDeposits := portal.Group("", api.requireUserReady())
	userDeposits.GET("/deposits/config", api.depositAvailability)
	userDeposits.GET("/deposits", api.listDeposits)
	userDeposits.POST("/deposits", api.createDeposit)
	userDeposits.GET("/deposits/:id", api.getDeposit)
	if callback != nil {
		router.POST("/api/v1/deposits/notify", gin.WrapH(callback))
	}
	userCards := portal.Group("", api.requireUserReady(), api.requireCards())
	userCards.GET("/cards/config", api.cardAvailability)
	userCards.GET("/cards", api.listUserCards)
	userCards.POST("/cards", api.createUserCard)
	userCards.GET("/cards/:id", api.getUserCard)
	userCards.POST("/cards/:id/sync", api.syncUserCard)
	userCards.GET("/card-operations/:id", api.getUserCardOperation)
	userCards.POST("/card-operations/:id/retry", api.retryUserCardOperation)
	router.NoRoute(func(request *gin.Context) { api.fail(request, 404, "not_found", "resource not found") })
	return router
}

func (api *API) requestContext() gin.HandlerFunc {
	return func(request *gin.Context) {
		requestID, err := uuid.Parse(request.GetHeader("X-Request-ID"))
		if err != nil {
			requestID = uuid.New()
		}
		request.Set("request_id", requestID.String())
		request.Header("X-Request-ID", requestID.String())
		request.Header("Cache-Control", "no-store")
		request.Header("X-Content-Type-Options", "nosniff")
		ctx, cancel := context.WithTimeout(request.Request.Context(), 10*time.Second)
		defer cancel()
		request.Request = request.Request.WithContext(ctx)
		started := time.Now()
		request.Next()
		api.logger.Info("http request", "request_id", requestID.String(), "method", request.Request.Method, "route", request.FullPath(), "status", request.Writer.Status(), "duration_ms", time.Since(started).Milliseconds())
	}
}

func (api *API) authenticate() gin.HandlerFunc {
	return func(request *gin.Context) {
		fields := strings.Fields(request.GetHeader("Authorization"))
		if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") {
			request.Header("WWW-Authenticate", "Bearer")
			api.handleError(request, domain.ErrUnauthorized)
			return
		}
		principal, err := api.auth.Authenticate(request.Request.Context(), fields[1])
		if err != nil {
			api.handleError(request, err)
			return
		}
		request.Set("principal", principal)
		request.Next()
	}
}

func (api *API) recover() gin.HandlerFunc {
	return func(request *gin.Context) {
		defer func() {
			if recovered := recover(); recovered != nil {
				api.logger.Error("request panic", "request_id", request.GetString("request_id"))
				api.fail(request, http.StatusInternalServerError, "internal_error", "internal server error")
			}
		}()
		request.Next()
	}
}

func (api *API) login(request *gin.Context) {
	input, ok := decode[struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}](api, request)
	if !ok {
		return
	}
	result, err := api.auth.Login(request.Request.Context(), input.Email, input.Password, request.ClientIP(), request.GetString("request_id"))
	if err != nil {
		api.handleError(request, err)
		return
	}
	api.success(request, http.StatusOK, result)
}

func (api *API) me(request *gin.Context) {
	api.success(request, http.StatusOK, principal(request).Admin)
}

func (api *API) logout(request *gin.Context) {
	if err := api.auth.Logout(request.Request.Context(), principal(request), request.GetString("request_id")); err != nil {
		api.handleError(request, err)
		return
	}
	api.success(request, http.StatusOK, gin.H{"logged_out": true})
}

func (api *API) createUser(request *gin.Context) {
	input, ok := decode[user.CreateInput](api, request)
	if !ok {
		return
	}
	result, err := api.users.Create(request.Request.Context(), principal(request).Admin.ID, request.GetString("request_id"), input)
	if err != nil {
		api.handleError(request, err)
		return
	}
	api.success(request, http.StatusCreated, result)
}

func (api *API) getUser(request *gin.Context) {
	userID, err := uuid.Parse(request.Param("id"))
	if err != nil {
		api.handleError(request, domain.ErrInvalid)
		return
	}
	result, err := api.users.Get(request.Request.Context(), userID)
	if err != nil {
		api.handleError(request, err)
		return
	}
	api.success(request, http.StatusOK, result)
}

func (api *API) updateUser(request *gin.Context) {
	userID, err := uuid.Parse(request.Param("id"))
	if err != nil {
		api.handleError(request, domain.ErrInvalid)
		return
	}
	input, ok := decode[user.UpdateInput](api, request)
	if !ok {
		return
	}
	result, err := api.users.Update(request.Request.Context(), principal(request).Admin.ID, request.GetString("request_id"), userID, input)
	if err != nil {
		api.handleError(request, err)
		return
	}
	api.success(request, http.StatusOK, result)
}

func (api *API) listUsers(request *gin.Context) {
	page, size, err := pagination(request)
	if err != nil {
		api.handleError(request, err)
		return
	}
	result, err := api.users.List(request.Request.Context(), page, size)
	if err != nil {
		api.handleError(request, err)
		return
	}
	api.success(request, http.StatusOK, result)
}

func (api *API) listAudit(request *gin.Context) {
	page, size, err := pagination(request)
	if err != nil {
		api.handleError(request, err)
		return
	}
	result, err := api.users.ListAudit(request.Request.Context(), page, size)
	if err != nil {
		api.handleError(request, err)
		return
	}
	api.success(request, http.StatusOK, result)
}

func principal(request *gin.Context) auth.Principal {
	return request.MustGet("principal").(auth.Principal)
}

func pagination(request *gin.Context) (int32, int32, error) {
	page, err := strconv.ParseInt(request.DefaultQuery("page", "1"), 10, 32)
	if err != nil {
		return 0, 0, domain.ErrInvalid
	}
	size, err := strconv.ParseInt(request.DefaultQuery("page_size", "20"), 10, 32)
	if err != nil || page < 1 || size < 1 || size > 100 || (page-1)*size > 2147483647 {
		return 0, 0, domain.ErrInvalid
	}
	return int32(page), int32(size), nil
}

func decode[Input any](api *API, request *gin.Context) (Input, bool) {
	var input Input
	contentType, _, err := mime.ParseMediaType(request.GetHeader("Content-Type"))
	if err != nil || contentType != "application/json" {
		api.fail(request, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
		return input, false
	}
	request.Request.Body = http.MaxBytesReader(request.Writer, request.Request.Body, 16*1024)
	decoder := json.NewDecoder(request.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		api.fail(request, http.StatusBadRequest, "invalid_input", "invalid JSON body")
		return input, false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		api.fail(request, http.StatusBadRequest, "invalid_input", "expected a single JSON object")
		return input, false
	}
	return input, true
}

func (api *API) success(request *gin.Context, status int, data any) {
	request.JSON(status, response{Data: data, RequestID: request.GetString("request_id")})
}

func (api *API) fail(request *gin.Context, status int, code, message string) {
	request.AbortWithStatusJSON(status, response{Error: &apiError{Code: code, Message: message}, RequestID: request.GetString("request_id")})
}

func (api *API) handleError(request *gin.Context, err error) {
	switch {
	case errors.Is(err, deposit.ErrDepositCapacity):
		api.fail(request, 409, "deposit_capacity", "resolve existing test deposits before creating more")
	case errors.Is(err, epusdt.ErrQuoteMismatch):
		api.fail(request, 400, "invalid_quote", "quote does not match saved order")
	case errors.Is(err, deposit.ErrDepositsDisabled):
		api.fail(request, 503, "deposits_disabled", "Nile deposits are not enabled or scanner is not ready")
	case errors.Is(err, domain.ErrIssuingDisabled):
		api.fail(request, 503, "issuing_disabled", "sandbox issuing is not configured")
	case errors.Is(err, domain.ErrProviderUnavailable):
		api.fail(request, 502, "provider_unavailable", "provider request failed; no automatic retry")
	case errors.Is(err, domain.ErrCardholderRequired):
		api.fail(request, 409, "cardholder_required", "ask administrator to bind a verified sandbox cardholder")
	case errors.Is(err, domain.ErrCardLimit):
		api.fail(request, 409, "card_limit_reached", "sandbox permits one issued or unresolved card per user")
	case errors.Is(err, domain.ErrIdempotencyConflict):
		api.fail(request, 409, "idempotency_conflict", "idempotency key used with different input")
	case errors.Is(err, domain.ErrPasswordChangeRequired):
		api.fail(request, 403, "password_change_required", "password change required")
	case errors.Is(err, domain.ErrInvalidState):
		api.fail(request, 409, "invalid_state", "operation not allowed in current state")
	case errors.Is(err, domain.ErrInvalid):
		api.fail(request, 400, "invalid_input", "invalid request parameters")
	case errors.Is(err, domain.ErrUnauthorized):
		api.fail(request, 401, "unauthorized", "invalid credentials or session")
	case errors.Is(err, domain.ErrNotFound):
		api.fail(request, 404, "not_found", "resource not found")
	case errors.Is(err, domain.ErrConflict):
		api.fail(request, 409, "conflict", "resource already exists")
	case errors.Is(err, domain.ErrRateLimited):
		request.Header("Retry-After", "60")
		api.fail(request, 429, "rate_limited", "too many login attempts")
	default:
		api.logger.Error("request failed", "request_id", request.GetString("request_id"), "route", request.FullPath())
		api.fail(request, 500, "internal_error", "internal server error")
	}
}
