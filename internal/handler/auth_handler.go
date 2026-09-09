package handler

import (
	"context"

	"connectrpc.com/connect"
	v1 "github.com/BeWellSpent/wellspent-backend/gen/wellspent/v1"
	"github.com/BeWellSpent/wellspent-backend/internal/service"
)

type AuthHandler struct {
	svc *service.AuthService
}

func NewAuthHandler(svc *service.AuthService) *AuthHandler {
	if svc == nil {
		panic("NewAuthHandler: svc is required")
	}
	return &AuthHandler{svc: svc}
}

func (h *AuthHandler) Register(ctx context.Context, req *connect.Request[v1.RegisterRequest]) (*connect.Response[v1.RegisterResponse], error) {
	result, err := h.svc.Register(ctx, req.Msg.Email, req.Msg.Password, req.Msg.FirstName, req.Msg.LastName, req.Msg.CountryCode, req.Msg.StateCode, req.Msg.Language, req.Msg.Currency, req.Msg.CaptchaToken)
	if err != nil {
		return nil, toConnectError(err)
	}
	return connect.NewResponse(&v1.RegisterResponse{
		AccessToken: result.AccessToken,
		TokenType:   "bearer",
		ExpiresIn:   result.ExpiresIn,
	}), nil
}

func (h *AuthHandler) Login(ctx context.Context, req *connect.Request[v1.LoginRequest]) (*connect.Response[v1.LoginResponse], error) {
	result, err := h.svc.Login(ctx, req.Msg.Email, req.Msg.Password, req.Msg.RememberMe)
	if err != nil {
		return nil, toConnectError(err)
	}
	return connect.NewResponse(&v1.LoginResponse{
		AccessToken: result.AccessToken,
		TokenType:   "bearer",
		ExpiresIn:   result.ExpiresIn,
		Language:    result.Language,
		Currency:    result.Currency,
	}), nil
}

func (h *AuthHandler) Logout(_ context.Context, _ *connect.Request[v1.LogoutRequest]) (*connect.Response[v1.LogoutResponse], error) {
	// JWT is stateless; invalidation is handled client-side by discarding the token.
	return connect.NewResponse(&v1.LogoutResponse{}), nil
}

func (h *AuthHandler) RefreshToken(_ context.Context, _ *connect.Request[v1.RefreshTokenRequest]) (*connect.Response[v1.RefreshTokenResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func (h *AuthHandler) GetGoogleAuthURL(_ context.Context, req *connect.Request[v1.GetGoogleAuthURLRequest]) (*connect.Response[v1.GetGoogleAuthURLResponse], error) {
	url := h.svc.GoogleAuthURL(req.Msg.State)
	return connect.NewResponse(&v1.GetGoogleAuthURLResponse{Url: url}), nil
}

func (h *AuthHandler) ExchangeGoogleCode(ctx context.Context, req *connect.Request[v1.ExchangeGoogleCodeRequest]) (*connect.Response[v1.ExchangeGoogleCodeResponse], error) {
	result, err := h.svc.GoogleExchange(ctx, req.Msg.Code, req.Msg.RedirectUri, req.Msg.Language, req.Msg.Currency)
	if err != nil {
		return nil, toConnectError(err)
	}
	return connect.NewResponse(&v1.ExchangeGoogleCodeResponse{
		AccessToken: result.AccessToken,
		ExpiresIn:   result.ExpiresIn,
		IsNewUser:   result.IsNewUser,
		Language:    result.Language,
		Currency:    result.Currency,
	}), nil
}

func (h *AuthHandler) SignInWithApple(ctx context.Context, req *connect.Request[v1.SignInWithAppleRequest]) (*connect.Response[v1.SignInWithAppleResponse], error) {
	result, err := h.svc.AppleSignIn(ctx, req.Msg.IdentityToken, req.Msg.AuthorizationCode, req.Msg.FirstName, req.Msg.LastName, req.Msg.Language, req.Msg.Currency)
	if err != nil {
		return nil, toConnectError(err)
	}
	return connect.NewResponse(&v1.SignInWithAppleResponse{
		AccessToken: result.AccessToken,
		ExpiresIn:   result.ExpiresIn,
		IsNewUser:   result.IsNewUser,
		Language:    result.Language,
		Currency:    result.Currency,
	}), nil
}

func (h *AuthHandler) VerifyEmail(ctx context.Context, req *connect.Request[v1.VerifyEmailRequest]) (*connect.Response[v1.VerifyEmailResponse], error) {
	if err := h.svc.VerifyEmail(ctx, req.Msg.Token); err != nil {
		return nil, toConnectError(err)
	}
	return connect.NewResponse(&v1.VerifyEmailResponse{Success: true}), nil
}

func (h *AuthHandler) ResendVerificationEmail(ctx context.Context, req *connect.Request[v1.ResendVerificationEmailRequest]) (*connect.Response[v1.ResendVerificationEmailResponse], error) {
	if err := h.svc.ResendVerificationEmail(ctx, req.Msg.Email); err != nil {
		return nil, toConnectError(err)
	}
	return connect.NewResponse(&v1.ResendVerificationEmailResponse{}), nil
}
