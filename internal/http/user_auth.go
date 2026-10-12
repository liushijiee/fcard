package httpapi

import (
	"strings"

	"fcard/internal/domain"
	"fcard/internal/userauth"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func (api *API) requireUserSession() gin.HandlerFunc {
	return func(request *gin.Context) {
		fields := strings.Fields(request.GetHeader("Authorization"))
		if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") {
			request.Header("WWW-Authenticate", "Bearer")
			api.handleError(request, domain.ErrUnauthorized)
			return
		}
		principal, err := api.userAuth.Authenticate(request.Request.Context(), fields[1])
		if err != nil {
			api.handleError(request, err)
			return
		}
		request.Set("user_principal", principal)
		request.Next()
	}
}

func (api *API) requireUserReady() gin.HandlerFunc {
	return func(request *gin.Context) {
		if userPrincipal(request).User.MustChangePassword {
			api.handleError(request, domain.ErrPasswordChangeRequired)
			return
		}
		request.Next()
	}
}

func userPrincipal(request *gin.Context) userauth.Principal {
	return request.MustGet("user_principal").(userauth.Principal)
}

func (api *API) userLogin(request *gin.Context) {
	input, ok := decode[struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}](api, request)
	if !ok {
		return
	}
	result, err := api.userAuth.Login(request.Request.Context(), input.Email, input.Password, request.ClientIP(), request.GetString("request_id"))
	if err != nil {
		api.handleError(request, err)
		return
	}
	api.success(request, 200, result)
}

func (api *API) userMe(request *gin.Context) { api.success(request, 200, userPrincipal(request).User) }

func (api *API) userLogout(request *gin.Context) {
	if err := api.userAuth.Logout(request.Request.Context(), userPrincipal(request), request.GetString("request_id")); err != nil {
		api.handleError(request, err)
		return
	}
	api.success(request, 200, gin.H{"logged_out": true})
}

func (api *API) changeUserPassword(request *gin.Context) {
	input, ok := decode[struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}](api, request)
	if !ok {
		return
	}
	if err := api.userAuth.ChangePassword(request.Request.Context(), userPrincipal(request), input.CurrentPassword, input.NewPassword, request.GetString("request_id")); err != nil {
		api.handleError(request, err)
		return
	}
	api.success(request, 200, gin.H{"password_changed": true, "reauthenticate": true})
}

func (api *API) resetUserPassword(request *gin.Context) {
	userID, err := uuid.Parse(request.Param("id"))
	if err != nil {
		api.handleError(request, domain.ErrInvalid)
		return
	}
	input, ok := decode[struct {
		TemporaryPassword string `json:"temporary_password"`
	}](api, request)
	if !ok {
		return
	}
	if err := api.userAuth.ResetPassword(request.Request.Context(), principal(request), userID, input.TemporaryPassword, request.GetString("request_id")); err != nil {
		api.handleError(request, err)
		return
	}
	api.success(request, 200, gin.H{"login_enabled": true, "must_change_password": true, "sessions_revoked": true})
}
