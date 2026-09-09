package service

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
	"unicode"

	"github.com/BeWellSpent/wellspent-backend/internal/apperr"
	"github.com/BeWellSpent/wellspent-backend/internal/auth"
	"github.com/BeWellSpent/wellspent-backend/internal/captcha"
	"github.com/BeWellSpent/wellspent-backend/internal/config"
	"github.com/BeWellSpent/wellspent-backend/internal/crypto"
	"github.com/BeWellSpent/wellspent-backend/internal/repository"
	db "github.com/BeWellSpent/wellspent-backend/internal/sqlc"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

// defaultTokenLifetime is the JWT lifetime for Login (remember_me=false),
// Register, and Google OAuth exchange — every auth flow except Login's
// remember-me path. Deliberately independent of JWTService's configured
// lifetime (JWT_LIFETIME_SECONDS): that value is a leftover default meant
// for GenerateToken() callers, not a lifetime any flow should silently
// inherit. Register and ExchangeGoogleCode used to call GenerateToken()
// and inherit whatever JWT_LIFETIME_SECONDS happened to be, which could
// be far shorter than Login's 24h — the cookie/token lifetimes matched
// (both now derive from the RPC's real expires_in), but the actual
// session length for those two flows was inconsistent with Login and
// with what the auth spec documents.
const (
	defaultTokenLifetime    = 24 * time.Hour
	rememberMeTokenLifetime = 90 * 24 * time.Hour

	// verificationResendCooldown throttles ResendVerificationEmail so a
	// user (or an attacker) can't trigger unlimited emails to an address.
	verificationResendCooldown = 60 * time.Second

	// oauth_account.oauth_name values. The table's UNIQUE (oauth_name,
	// account_id) already namespaces providers, so each provider's subject
	// only ever collides with itself.
	oauthProviderGoogle = "google"
	oauthProviderApple  = "apple"
)

type AuthService struct {
	users   repository.UserRepository
	jwt     *auth.JWTService
	google  *auth.GoogleOAuth
	apple   auth.AppleAuthenticator
	captcha captcha.Verifier
	cfg     *config.Config
	log     *zap.Logger
	mailer  *VerificationMailer
}

func NewAuthService(users repository.UserRepository, jwt *auth.JWTService, google *auth.GoogleOAuth, apple auth.AppleAuthenticator, captchaVerifier captcha.Verifier, cfg *config.Config, log *zap.Logger) *AuthService {
	if users == nil {
		panic("NewAuthService: users is required")
	}
	if jwt == nil {
		panic("NewAuthService: jwt is required")
	}
	if google == nil {
		panic("NewAuthService: google is required")
	}
	if apple == nil {
		panic("NewAuthService: apple is required")
	}
	if captchaVerifier == nil {
		panic("NewAuthService: captchaVerifier is required")
	}
	if cfg == nil {
		panic("NewAuthService: cfg is required")
	}
	if log == nil {
		panic("NewAuthService: log is required")
	}
	return &AuthService{
		users:   users,
		jwt:     jwt,
		google:  google,
		apple:   apple,
		captcha: captchaVerifier,
		cfg:     cfg,
		log:     log,
		// Built here rather than injected: every dependency it needs is
		// already a required argument, so an extra parameter would add
		// churn at every call site for no added flexibility.
		mailer: NewVerificationMailer(users, cfg, log),
	}
}

type LoginResult struct {
	AccessToken string
	ExpiresIn   int64
	Language    string
	Currency    string
}

func (s *AuthService) Login(ctx context.Context, email, password string, rememberMe bool) (LoginResult, error) {
	user, err := s.users.GetByEmail(ctx, email)
	if err != nil {
		// Surface as generic error to avoid email enumeration
		return LoginResult{}, apperr.Invalid("invalid email or password")
	}
	if !user.IsActive {
		if user.Status == "disabled" {
			return LoginResult{}, apperr.Forbidden("account is deactivated — contact support to recover your account")
		}
		return LoginResult{}, apperr.Forbidden("account is inactive")
	}
	if user.HashedPassword == nil {
		return LoginResult{}, apperr.Invalid("account uses OAuth login only")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(*user.HashedPassword), []byte(password)); err != nil {
		return LoginResult{}, apperr.Invalid("invalid email or password")
	}

	lifetime := defaultTokenLifetime
	if rememberMe {
		lifetime = rememberMeTokenLifetime
	}
	token, err := s.jwt.GenerateTokenWithLifetime(user.ID, lifetime)
	if err != nil {
		return LoginResult{}, fmt.Errorf("auth: generate token: %w", err)
	}
	return LoginResult{AccessToken: token, ExpiresIn: int64(lifetime.Seconds()), Language: user.Language, Currency: user.Currency}, nil
}

type GoogleExchangeResult struct {
	AccessToken string
	ExpiresIn   int64
	IsNewUser   bool
	Language    string
	Currency    string
}

func (s *AuthService) GoogleExchange(ctx context.Context, code, redirectURI, language, currency string) (GoogleExchangeResult, error) {
	info, _, err := s.google.Exchange(ctx, code)
	if err != nil {
		return GoogleExchangeResult{}, fmt.Errorf("auth: google exchange: %w", err)
	}

	// Check if OAuth account already linked
	oauthAcc, err := s.users.GetOAuthAccount(ctx, db.GetOAuthAccountParams{
		OauthName: oauthProviderGoogle,
		AccountID: info.Sub,
	})

	var userID uuid.UUID
	var userLang, userCurrency string
	isNew := false

	if err != nil {
		var notFound *apperr.NotFoundError
		if !errors.As(err, &notFound) {
			return GoogleExchangeResult{}, err
		}
		// New OAuth user — try to find existing user by email or create
		user, err := s.users.GetByEmail(ctx, info.Email)
		if err != nil {
			var notFoundUser *apperr.NotFoundError
			if !errors.As(err, &notFoundUser) {
				return GoogleExchangeResult{}, err
			}
			// Create brand new user
			lang := language
			if lang == "" {
				lang = "en"
			}
			cur := currency
			if cur == "" {
				cur = "USD"
			}
			user, err = s.users.Create(ctx, db.CreateUserParams{
				Email:     info.Email,
				FirstName: &info.GivenName,
				LastName:  &info.FamilyName,
				Language:  lang,
				Currency:  cur,
			})
			if err != nil {
				return GoogleExchangeResult{}, fmt.Errorf("auth: create user: %w", err)
			}
			// Google already proved ownership of this email address —
			// skip the token/email verification flow entirely.
			if err := s.users.MarkVerified(ctx, user.ID); err != nil {
				return GoogleExchangeResult{}, fmt.Errorf("auth: mark verified: %w", err)
			}
			isNew = true
		}
		if !isNew {
			// Existing email/password user linking Google for the first time.
			// Google proved ownership of this address — auto-verify the account.
			if verifyErr := s.users.MarkVerified(ctx, user.ID); verifyErr != nil {
				return GoogleExchangeResult{}, fmt.Errorf("auth: mark verified: %w", verifyErr)
			}
		}
		userID = user.ID
		userLang = user.Language
		userCurrency = user.Currency
		// Link OAuth account
		_, err = s.users.CreateOAuthAccount(ctx, db.CreateOAuthAccountParams{
			UserID:       userID,
			OauthName:    oauthProviderGoogle,
			AccountID:    info.Sub,
			AccountEmail: info.Email,
		})
		if err != nil {
			return GoogleExchangeResult{}, fmt.Errorf("auth: link oauth account: %w", err)
		}
	} else {
		userID = oauthAcc.UserID
		existing, fetchErr := s.users.GetByID(ctx, userID)
		if fetchErr != nil {
			return GoogleExchangeResult{}, fmt.Errorf("auth: get user: %w", fetchErr)
		}
		userLang = existing.Language
		userCurrency = existing.Currency
	}

	// Google OAuth has no "remember me" UI — always issue a persistent token
	// so the session lifetime matches what a user would get by checking
	// "remember me" on the email/password login form.
	token, err := s.jwt.GenerateTokenWithLifetime(userID, rememberMeTokenLifetime)
	if err != nil {
		return GoogleExchangeResult{}, fmt.Errorf("auth: generate token: %w", err)
	}
	return GoogleExchangeResult{AccessToken: token, ExpiresIn: int64(rememberMeTokenLifetime.Seconds()), IsNewUser: isNew, Language: userLang, Currency: userCurrency}, nil
}

func (s *AuthService) GoogleAuthURL(state string) string {
	return s.google.AuthCodeURL(state)
}

type AppleSignInResult struct {
	AccessToken string
	ExpiresIn   int64
	IsNewUser   bool
	Language    string
	Currency    string
}

// AppleSignIn resolves a native Sign in with Apple credential to a session.
//
// Account resolution mirrors GoogleExchange: look the user up by the
// provider's stable subject first, fall back to matching an existing account
// by email, and only create a new user when neither hits. The email fallback
// is what makes "already signed up with Google, same address" land on the
// existing account instead of a duplicate.
//
// firstName/lastName come from the request rather than the token because
// Apple returns the user's name only on the very first authorization and
// never again — so they are written at creation time and never overwritten.
func (s *AuthService) AppleSignIn(ctx context.Context, identityToken, authorizationCode, firstName, lastName, language, currency string) (AppleSignInResult, error) {
	identity, err := s.apple.VerifyIdentityToken(ctx, identityToken)
	if err != nil {
		s.log.Warn("auth.apple.verify_failed", zap.Error(err))
		return AppleSignInResult{}, apperr.Invalid("invalid Apple identity token")
	}
	if identity.Email == "" {
		return AppleSignInResult{}, apperr.Invalid("Apple identity token contained no email address")
	}

	var (
		userID       uuid.UUID
		userLang     string
		userCurrency string
		oauthID      uuid.UUID
		isNew        bool
	)

	oauthAcc, err := s.users.GetOAuthAccount(ctx, db.GetOAuthAccountParams{
		OauthName: oauthProviderApple,
		AccountID: identity.Sub,
	})
	switch {
	case err == nil:
		// Returning user — resolved by Apple's stable subject.
		existing, fetchErr := s.users.GetByID(ctx, oauthAcc.UserID)
		if fetchErr != nil {
			return AppleSignInResult{}, fmt.Errorf("auth: get user: %w", fetchErr)
		}
		if !existing.IsActive {
			return AppleSignInResult{}, apperr.Forbidden("account is inactive")
		}
		userID = existing.ID
		userLang = existing.Language
		userCurrency = existing.Currency
		oauthID = oauthAcc.ID

	default:
		var notFound *apperr.NotFoundError
		if !errors.As(err, &notFound) {
			return AppleSignInResult{}, err
		}

		user, lookupErr := s.users.GetByEmail(ctx, identity.Email)
		if lookupErr != nil {
			var notFoundUser *apperr.NotFoundError
			if !errors.As(lookupErr, &notFoundUser) {
				return AppleSignInResult{}, lookupErr
			}
			lang := language
			if lang == "" {
				lang = "en"
			}
			cur := currency
			if cur == "" {
				cur = "USD"
			}
			user, err = s.users.Create(ctx, db.CreateUserParams{
				Email:     identity.Email,
				FirstName: &firstName,
				LastName:  &lastName,
				Language:  lang,
				Currency:  cur,
			})
			if err != nil {
				return AppleSignInResult{}, fmt.Errorf("auth: create user: %w", err)
			}
			isNew = true
		} else if !identity.EmailVerified {
			// Linking to a pre-existing account on the strength of an
			// unverified email claim would let anyone who can mint such a
			// claim take over that account. Creating a fresh account is
			// safe; adopting someone else's is not.
			s.log.Warn("auth.apple.unverified_email_link_rejected", zap.String("email", identity.Email))
			return AppleSignInResult{}, apperr.Invalid("Apple did not confirm this email address")
		} else if !user.IsActive {
			return AppleSignInResult{}, apperr.Forbidden("account is inactive")
		}

		// Apple vouched for this address, so skip the email-verification
		// round trip — same reasoning as the Google path.
		if err := s.users.MarkVerified(ctx, user.ID); err != nil {
			return AppleSignInResult{}, fmt.Errorf("auth: mark verified: %w", err)
		}

		created, err := s.users.CreateOAuthAccount(ctx, db.CreateOAuthAccountParams{
			UserID:       user.ID,
			OauthName:    oauthProviderApple,
			AccountID:    identity.Sub,
			AccountEmail: identity.Email,
		})
		if err != nil {
			return AppleSignInResult{}, fmt.Errorf("auth: link oauth account: %w", err)
		}
		userID = user.ID
		userLang = user.Language
		userCurrency = user.Currency
		oauthID = created.ID
	}

	s.storeAppleRefreshToken(ctx, oauthID, authorizationCode)

	// No "remember me" control exists on either OAuth flow, so both issue the
	// long-lived token a user would get by ticking it on the login form.
	token, err := s.jwt.GenerateTokenWithLifetime(userID, rememberMeTokenLifetime)
	if err != nil {
		return AppleSignInResult{}, fmt.Errorf("auth: generate token: %w", err)
	}
	return AppleSignInResult{
		AccessToken: token,
		ExpiresIn:   int64(rememberMeTokenLifetime.Seconds()),
		IsNewUser:   isNew,
		Language:    userLang,
		Currency:    userCurrency,
	}, nil
}

// storeAppleRefreshToken exchanges the one-time authorization code for a
// refresh token and persists it encrypted, so the account can be revoked with
// Apple on deletion.
//
// Deliberately best-effort and never returns an error: failing a sign-in
// because Apple's token endpoint was briefly unavailable is a far worse
// outcome than a missing revocation token, and the next sign-in supplies a
// fresh code to retry with.
func (s *AuthService) storeAppleRefreshToken(ctx context.Context, oauthAccountID uuid.UUID, authorizationCode string) {
	if strings.TrimSpace(authorizationCode) == "" {
		return
	}
	refreshToken, err := s.apple.ExchangeCode(ctx, authorizationCode)
	if err != nil {
		if errors.Is(err, auth.ErrAppleKeyNotConfigured) {
			s.log.Warn("auth.apple.exchange_skipped: APPLE_KEY_ID/APPLE_PRIVATE_KEY unset, account cannot be revoked with Apple on deletion")
		} else {
			s.log.Error("auth.apple.exchange_failed", zap.Error(err))
		}
		return
	}
	if s.cfg.EncryptionKey == "" {
		s.log.Error("auth.apple.refresh_token_not_stored: ENCRYPTION_KEY unset")
		return
	}
	encrypted, err := crypto.Encrypt(refreshToken, s.cfg.EncryptionKey)
	if err != nil {
		s.log.Error("auth.apple.encrypt_refresh_token_failed", zap.Error(err))
		return
	}
	if err := s.users.UpdateOAuthAccountRefreshToken(ctx, db.UpdateOAuthAccountRefreshTokenParams{
		ID:           oauthAccountID,
		RefreshToken: &encrypted,
	}); err != nil {
		s.log.Error("auth.apple.store_refresh_token_failed", zap.Error(err))
	}
}

type RegisterResult struct {
	AccessToken string
	ExpiresIn   int64
}

func (s *AuthService) Register(ctx context.Context, email, password, firstName, lastName, countryCode, stateCode, language, currency, captchaToken string) (RegisterResult, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if _, err := mail.ParseAddress(email); err != nil {
		return RegisterResult{}, apperr.Invalid("invalid email address")
	}
	if err := validatePassword(password); err != nil {
		return RegisterResult{}, err
	}

	// Cheap local checks first, then the external network call, then the DB
	// round-trip below — no point spending a Cloudflare request on a request
	// that was already going to fail format validation. "Couldn't verify"
	// (a Cloudflare/network failure) and "verification failed" (a real
	// rejection) are deliberately treated the same here: Register has no use
	// for the distinction, and failing open on a captcha *outage* would
	// defeat the entire point of having one.
	//
	// Gated on CaptchaEnforcementEnabled (default false) — see that field's
	// doc comment in config.go for why this can't just always be on.
	if s.cfg.CaptchaEnforcementEnabled {
		if ok, err := s.captcha.Verify(ctx, captchaToken, ""); err != nil || !ok {
			if err != nil {
				s.log.Warn("auth.register.captcha_verify_error", zap.Error(err))
			}
			return RegisterResult{}, apperr.Invalid("captcha verification failed")
		}
	}

	_, err := s.users.GetByEmail(ctx, email)
	if err == nil {
		return RegisterResult{}, apperr.Duplicate("user", "email", email)
	}
	var notFound *apperr.NotFoundError
	if !errors.As(err, &notFound) {
		return RegisterResult{}, err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return RegisterResult{}, fmt.Errorf("auth: hash password: %w", err)
	}
	hashed := string(hash)
	fn := firstName
	ln := lastName
	lang := language
	if lang == "" {
		lang = "en"
	}
	cur := currency
	if cur == "" {
		cur = "USD"
	}
	params := db.CreateUserParams{
		Email:          email,
		HashedPassword: &hashed,
		FirstName:      &fn,
		LastName:       &ln,
		Language:       lang,
		Currency:       cur,
	}
	if countryCode != "" {
		params.CountryCode = &countryCode
	}
	if stateCode != "" {
		params.StateCode = &stateCode
	}
	user, err := s.users.Create(ctx, params)
	if err != nil {
		return RegisterResult{}, fmt.Errorf("auth: create user: %w", err)
	}

	// Best-effort — the account is already created; a failed send just
	// means the user falls back to "resend verification email" later.
	if err := s.mailer.Send(ctx, user); err != nil {
		s.log.Error("auth.verification_email.failed", zap.String("to", user.Email), zap.Error(err))
	}

	token, err := s.jwt.GenerateTokenWithLifetime(user.ID, defaultTokenLifetime)
	if err != nil {
		return RegisterResult{}, fmt.Errorf("auth: generate token: %w", err)
	}
	return RegisterResult{AccessToken: token, ExpiresIn: int64(defaultTokenLifetime.Seconds())}, nil
}

// VerifyEmail redeems a verification token minted by sendVerificationEmail.
// Errors are deliberately generic (mirrors Login's invalid-credentials
// message) so a caller can't distinguish "wrong token" from "expired token"
// from "never existed".
func (s *AuthService) VerifyEmail(ctx context.Context, token string) error {
	tok, err := uuid.Parse(token)
	if err != nil {
		return apperr.Invalid("invalid or expired verification token")
	}
	user, err := s.users.GetByVerificationToken(ctx, tok)
	if err != nil {
		var notFound *apperr.NotFoundError
		if errors.As(err, &notFound) {
			return apperr.Invalid("invalid or expired verification token")
		}
		return err
	}
	if !user.EmailVerificationExpiresAt.Valid || user.EmailVerificationExpiresAt.Time.Before(time.Now().UTC()) {
		return apperr.Invalid("invalid or expired verification token")
	}
	if err := s.users.MarkVerified(ctx, user.ID); err != nil {
		return fmt.Errorf("auth: mark verified: %w", err)
	}
	return nil
}

// ResendVerificationEmail re-mints and re-sends a verification token.
// Always succeeds for unknown or already-verified emails (no error) to
// avoid leaking account existence/status — only a too-soon repeat request
// for a real, unverified account is rejected, matching the cooldown.
func (s *AuthService) ResendVerificationEmail(ctx context.Context, email string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	user, err := s.users.GetByEmail(ctx, email)
	if err != nil {
		var notFound *apperr.NotFoundError
		if errors.As(err, &notFound) {
			return nil
		}
		return err
	}
	if user.IsVerified {
		return nil
	}
	if user.EmailVerificationLastSentAt.Valid &&
		time.Since(user.EmailVerificationLastSentAt.Time) < verificationResendCooldown {
		return apperr.Invalid("please wait before requesting another verification email")
	}
	return s.mailer.Send(ctx, user)
}

func validatePassword(password string) error {
	if len(password) < 8 {
		return apperr.Invalid("password must be at least 8 characters")
	}
	var hasUpper, hasLower, hasDigit, hasSpecial bool
	for _, c := range password {
		switch {
		case unicode.IsUpper(c):
			hasUpper = true
		case unicode.IsLower(c):
			hasLower = true
		case unicode.IsDigit(c):
			hasDigit = true
		case unicode.IsPunct(c) || unicode.IsSymbol(c):
			hasSpecial = true
		}
	}
	if !hasUpper || !hasLower || !hasDigit || !hasSpecial {
		return apperr.Invalid("password must contain uppercase, lowercase, digit, and special character")
	}
	return nil
}
