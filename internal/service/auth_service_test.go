package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/BeWellSpent/wellspent-backend/internal/apperr"
	"github.com/BeWellSpent/wellspent-backend/internal/auth"
	"github.com/BeWellSpent/wellspent-backend/internal/captcha"
	"github.com/BeWellSpent/wellspent-backend/internal/config"
	"github.com/BeWellSpent/wellspent-backend/internal/crypto"
	db "github.com/BeWellSpent/wellspent-backend/internal/sqlc"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

// ── Mock ──────────────────────────────────────────────────────────────────────

type mockUserRepo struct {
	getByEmail           func(context.Context, string) (db.User, error)
	getByID              func(context.Context, uuid.UUID) (db.User, error)
	create               func(context.Context, db.CreateUserParams) (db.User, error)
	update               func(context.Context, db.UpdateUserParams) (db.User, error)
	updatePassword       func(context.Context, db.UpdateUserPasswordParams) error
	updateEmail          func(context.Context, db.UpdateUserEmailParams) (db.User, error)
	delete               func(context.Context, uuid.UUID) error
	softDelete           func(context.Context, uuid.UUID) error
	getOAuth             func(context.Context, db.GetOAuthAccountParams) (db.OauthAccount, error)
	createOAuth          func(context.Context, db.CreateOAuthAccountParams) (db.OauthAccount, error)
	updateOAuthRefresh   func(context.Context, db.UpdateOAuthAccountRefreshTokenParams) error
	listOAuthByUser      func(context.Context, uuid.UUID) ([]db.OauthAccount, error)
	listEnabledCountries func(context.Context) ([]db.ListEnabledCountriesRow, error)
	listCountryFeatures  func(context.Context) ([]db.CountryFeature, error)
	listTestAccounts     func(context.Context) ([]db.ListTestAccountsRow, error)
	setEmailVerification func(context.Context, db.SetEmailVerificationTokenParams) (db.User, error)
	getByVerification    func(context.Context, uuid.UUID) (db.User, error)
	markVerified         func(context.Context, uuid.UUID) error
}

func (m *mockUserRepo) GetByEmail(ctx context.Context, email string) (db.User, error) {
	if m.getByEmail != nil {
		return m.getByEmail(ctx, email)
	}
	return db.User{}, apperr.NotFound("user", email)
}

func (m *mockUserRepo) GetByID(ctx context.Context, id uuid.UUID) (db.User, error) {
	if m.getByID != nil {
		return m.getByID(ctx, id)
	}
	return db.User{}, apperr.NotFound("user", id.String())
}

func (m *mockUserRepo) Create(ctx context.Context, arg db.CreateUserParams) (db.User, error) {
	if m.create != nil {
		return m.create(ctx, arg)
	}
	return db.User{ID: uuid.New(), Email: arg.Email, IsActive: true}, nil
}

func (m *mockUserRepo) Update(ctx context.Context, arg db.UpdateUserParams) (db.User, error) {
	if m.update != nil {
		return m.update(ctx, arg)
	}
	return db.User{}, nil
}

func (m *mockUserRepo) UpdatePassword(ctx context.Context, arg db.UpdateUserPasswordParams) error {
	if m.updatePassword != nil {
		return m.updatePassword(ctx, arg)
	}
	return nil
}

func (m *mockUserRepo) ListTestAccounts(ctx context.Context) ([]db.ListTestAccountsRow, error) {
	if m.listTestAccounts != nil {
		return m.listTestAccounts(ctx)
	}
	return nil, nil
}

func (m *mockUserRepo) UpdateEmail(ctx context.Context, arg db.UpdateUserEmailParams) (db.User, error) {
	if m.updateEmail != nil {
		return m.updateEmail(ctx, arg)
	}
	return db.User{ID: arg.ID, Email: arg.Email}, nil
}

func (m *mockUserRepo) Delete(ctx context.Context, id uuid.UUID) error {
	if m.delete != nil {
		return m.delete(ctx, id)
	}
	return nil
}

func (m *mockUserRepo) SoftDelete(ctx context.Context, id uuid.UUID) error {
	if m.softDelete != nil {
		return m.softDelete(ctx, id)
	}
	return nil
}

func (m *mockUserRepo) GetOAuthAccount(ctx context.Context, arg db.GetOAuthAccountParams) (db.OauthAccount, error) {
	if m.getOAuth != nil {
		return m.getOAuth(ctx, arg)
	}
	return db.OauthAccount{}, apperr.NotFound("oauth_account", arg.AccountID)
}

func (m *mockUserRepo) CreateOAuthAccount(ctx context.Context, arg db.CreateOAuthAccountParams) (db.OauthAccount, error) {
	if m.createOAuth != nil {
		return m.createOAuth(ctx, arg)
	}
	return db.OauthAccount{ID: uuid.New(), UserID: arg.UserID}, nil
}

func (m *mockUserRepo) ListEnabledCountries(ctx context.Context) ([]db.ListEnabledCountriesRow, error) {
	if m.listEnabledCountries != nil {
		return m.listEnabledCountries(ctx)
	}
	return nil, nil
}

func (m *mockUserRepo) ListCountryFeatures(ctx context.Context) ([]db.CountryFeature, error) {
	if m.listCountryFeatures != nil {
		return m.listCountryFeatures(ctx)
	}
	return nil, nil
}

func (m *mockUserRepo) SetEmailVerificationToken(ctx context.Context, arg db.SetEmailVerificationTokenParams) (db.User, error) {
	if m.setEmailVerification != nil {
		return m.setEmailVerification(ctx, arg)
	}
	return db.User{ID: arg.ID}, nil
}

func (m *mockUserRepo) GetByVerificationToken(ctx context.Context, token uuid.UUID) (db.User, error) {
	if m.getByVerification != nil {
		return m.getByVerification(ctx, token)
	}
	return db.User{}, apperr.NotFound("user", "verification_token")
}

func (m *mockUserRepo) MarkVerified(ctx context.Context, id uuid.UUID) error {
	if m.markVerified != nil {
		return m.markVerified(ctx, id)
	}
	return nil
}

func (m *mockUserRepo) UpdateOAuthAccountRefreshToken(ctx context.Context, arg db.UpdateOAuthAccountRefreshTokenParams) error {
	if m.updateOAuthRefresh != nil {
		return m.updateOAuthRefresh(ctx, arg)
	}
	return nil
}

func (m *mockUserRepo) ListOAuthAccountsByUser(ctx context.Context, userID uuid.UUID) ([]db.OauthAccount, error) {
	if m.listOAuthByUser != nil {
		return m.listOAuthByUser(ctx, userID)
	}
	return nil, nil
}

// mockAppleAuth stands in for Apple's endpoints. Unlike GoogleOAuth (a
// concrete struct, hence untestable), the Apple client sits behind an
// interface precisely so account resolution can be exercised offline.
type mockAppleAuth struct {
	verify   func(context.Context, string) (auth.AppleIdentity, error)
	exchange func(context.Context, string) (string, error)
	revoke   func(context.Context, string) error
}

func (m *mockAppleAuth) VerifyIdentityToken(ctx context.Context, token string) (auth.AppleIdentity, error) {
	if m.verify != nil {
		return m.verify(ctx, token)
	}
	return auth.AppleIdentity{Sub: "apple-sub", Email: "apple@example.com", EmailVerified: true}, nil
}

func (m *mockAppleAuth) ExchangeCode(ctx context.Context, code string) (string, error) {
	if m.exchange != nil {
		return m.exchange(ctx, code)
	}
	return "refresh-token", nil
}

func (m *mockAppleAuth) RevokeRefreshToken(ctx context.Context, token string) error {
	if m.revoke != nil {
		return m.revoke(ctx, token)
	}
	return nil
}

// mockCaptchaVerifier defaults to "always passes" — most Register tests
// aren't testing captcha behavior and shouldn't have to think about it.
type mockCaptchaVerifier struct {
	verify func(ctx context.Context, token, remoteIP string) (bool, error)
}

func (m *mockCaptchaVerifier) Verify(ctx context.Context, token, remoteIP string) (bool, error) {
	if m.verify != nil {
		return m.verify(ctx, token, remoteIP)
	}
	return true, nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func testJWT() *auth.JWTService {
	return auth.NewJWTService("test-secret-32-chars-minimum-ok!")
}

func newAuthSvc(repo *mockUserRepo) *AuthService {
	return newAuthSvcWithApple(repo, &mockAppleAuth{})
}

func newAuthSvcWithApple(repo *mockUserRepo, apple auth.AppleAuthenticator) *AuthService {
	return newAuthSvcWithAppleAndCaptcha(repo, apple, &mockCaptchaVerifier{})
}

func newAuthSvcWithCaptcha(repo *mockUserRepo, verifier captcha.Verifier) *AuthService {
	return newAuthSvcWithAppleAndCaptcha(repo, &mockAppleAuth{}, verifier)
}

func newAuthSvcWithAppleAndCaptcha(repo *mockUserRepo, apple auth.AppleAuthenticator, verifier captcha.Verifier) *AuthService {
	// Empty ResendAPIKey routes sendVerificationEmail into its
	// no-op "skipped" branch, so tests don't need a real Resend client.
	// Empty Google OAuth credentials are fine — no test exercises the OAuth flow.
	// EncryptionKey is a valid 32-byte hex key so the Apple refresh-token
	// storage path runs for real rather than bailing out early.
	// CaptchaEnforcementEnabled: true so tests exercise the real check
	// (matching dev/.env.dev) — CaptchaRejected/VerifierError/etc. below
	// would be no-ops against the false default.
	cfg := &config.Config{EncryptionKey: strings.Repeat("ab", 32), CaptchaEnforcementEnabled: true}
	return NewAuthService(repo, testJWT(), auth.NewGoogleOAuth("", "", ""), apple, verifier, cfg, zap.NewNop())
}

func hashFor(t *testing.T, password string) string {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	require.NoError(t, err)
	return string(h)
}

// ── Register ──────────────────────────────────────────────────────────────────

func TestRegister_Success(t *testing.T) {
	repo := &mockUserRepo{}
	result, err := newAuthSvc(repo).Register(context.Background(), "new@example.com", "Strong@1", "Jane", "Doe", "", "", "", "", "")
	require.NoError(t, err)
	assert.NotEmpty(t, result.AccessToken)
	assert.Equal(t, int64(24*3600), result.ExpiresIn)
}

func TestRegister_CaptchaTokenPassedThroughToVerifier(t *testing.T) {
	var gotToken string
	verifier := &mockCaptchaVerifier{
		verify: func(_ context.Context, token, _ string) (bool, error) {
			gotToken = token
			return true, nil
		},
	}
	_, err := newAuthSvcWithCaptcha(&mockUserRepo{}, verifier).
		Register(context.Background(), "new@example.com", "Strong@1", "", "", "", "", "", "", "the-real-token")

	require.NoError(t, err)
	assert.Equal(t, "the-real-token", gotToken)
}

func TestRegister_CaptchaRejected_FailsWithoutCreatingAnAccount(t *testing.T) {
	created := false
	repo := &mockUserRepo{
		create: func(_ context.Context, arg db.CreateUserParams) (db.User, error) {
			created = true
			return db.User{ID: uuid.New(), Email: arg.Email, IsActive: true}, nil
		},
	}
	verifier := &mockCaptchaVerifier{verify: func(context.Context, string, string) (bool, error) { return false, nil }}

	_, err := newAuthSvcWithCaptcha(repo, verifier).
		Register(context.Background(), "new@example.com", "Strong@1", "", "", "", "", "", "", "bad-token")

	require.Error(t, err)
	var ve *apperr.ValidationError
	require.ErrorAs(t, err, &ve)
	assert.False(t, created, "a rejected captcha must not reach account creation")
}

// A Cloudflare outage or network failure must fail closed, not open — a
// captcha nobody can be checked against is not the same as no captcha at
// all, and failing open here would defeat the entire point of having one.
func TestRegister_CaptchaVerifierError_FailsClosed(t *testing.T) {
	created := false
	repo := &mockUserRepo{
		create: func(_ context.Context, arg db.CreateUserParams) (db.User, error) {
			created = true
			return db.User{ID: uuid.New(), Email: arg.Email, IsActive: true}, nil
		},
	}
	verifier := &mockCaptchaVerifier{verify: func(context.Context, string, string) (bool, error) {
		return false, errors.New("cloudflare unreachable")
	}}

	_, err := newAuthSvcWithCaptcha(repo, verifier).
		Register(context.Background(), "new@example.com", "Strong@1", "", "", "", "", "", "", "some-token")

	require.Error(t, err)
	assert.False(t, created)
}

// CaptchaEnforcementEnabled defaults to false — the backend and the web
// widget that submits captcha_token are two independent deploys, so
// enforcement must stay off until it's confirmed the widget is actually
// live, not the moment this code merges. A rejected/empty token must not
// block registration while the flag is off.
func TestRegister_EnforcementDisabledByDefault_IgnoresARejectedCaptcha(t *testing.T) {
	verifier := &mockCaptchaVerifier{verify: func(context.Context, string, string) (bool, error) { return false, nil }}
	cfg := &config.Config{EncryptionKey: strings.Repeat("ab", 32)} // CaptchaEnforcementEnabled left at its false zero value
	svc := NewAuthService(&mockUserRepo{}, testJWT(), auth.NewGoogleOAuth("", "", ""), &mockAppleAuth{}, verifier, cfg, zap.NewNop())

	_, err := svc.Register(context.Background(), "new@example.com", "Strong@1", "", "", "", "", "", "", "")

	require.NoError(t, err)
}

// Cheap local validation (email format) must not spend a Cloudflare request
// on a request that was already going to fail.
func TestRegister_InvalidEmail_NeverCallsTheCaptchaVerifier(t *testing.T) {
	called := false
	verifier := &mockCaptchaVerifier{verify: func(context.Context, string, string) (bool, error) {
		called = true
		return true, nil
	}}

	_, err := newAuthSvcWithCaptcha(&mockUserRepo{}, verifier).
		Register(context.Background(), "not-an-email", "Strong@1", "", "", "", "", "", "", "tok")

	require.Error(t, err)
	assert.False(t, called)
}

func TestRegister_EmailNormalized(t *testing.T) {
	var capturedEmail string
	repo := &mockUserRepo{
		create: func(_ context.Context, arg db.CreateUserParams) (db.User, error) {
			capturedEmail = arg.Email
			return db.User{ID: uuid.New(), Email: arg.Email, IsActive: true}, nil
		},
	}
	_, err := newAuthSvc(repo).Register(context.Background(), "  USER@Example.COM  ", "Strong@1", "", "", "", "", "", "", "")
	require.NoError(t, err)
	assert.Equal(t, "user@example.com", capturedEmail)
}

func TestRegister_InvalidEmail(t *testing.T) {
	_, err := newAuthSvc(&mockUserRepo{}).Register(context.Background(), "not-an-email", "Strong@1", "", "", "", "", "", "", "")
	require.Error(t, err)
	var ve *apperr.ValidationError
	require.ErrorAs(t, err, &ve)
}

func TestRegister_SendsVerificationToken(t *testing.T) {
	var captured db.SetEmailVerificationTokenParams
	var called bool
	repo := &mockUserRepo{
		setEmailVerification: func(_ context.Context, arg db.SetEmailVerificationTokenParams) (db.User, error) {
			called = true
			captured = arg
			return db.User{ID: arg.ID}, nil
		},
	}
	result, err := newAuthSvc(repo).Register(context.Background(), "new@example.com", "Strong@1", "Jane", "Doe", "", "", "", "", "")
	require.NoError(t, err)
	assert.NotEmpty(t, result.AccessToken)
	require.True(t, called, "expected a verification token to be minted")
	assert.NotNil(t, captured.Token)
	require.True(t, captured.ExpiresAt.Valid)
	assert.WithinDuration(t, time.Now().UTC().Add(emailVerificationTTL), captured.ExpiresAt.Time, 5*time.Second)
}

func TestRegister_VerificationEmailFailure_DoesNotFailRegistration(t *testing.T) {
	repo := &mockUserRepo{
		setEmailVerification: func(_ context.Context, _ db.SetEmailVerificationTokenParams) (db.User, error) {
			return db.User{}, assert.AnError
		},
	}
	result, err := newAuthSvc(repo).Register(context.Background(), "new@example.com", "Strong@1", "", "", "", "", "", "", "")
	require.NoError(t, err)
	assert.NotEmpty(t, result.AccessToken)
}

func TestRegister_DuplicateEmail(t *testing.T) {
	repo := &mockUserRepo{
		getByEmail: func(_ context.Context, email string) (db.User, error) {
			return db.User{Email: email, IsActive: true}, nil
		},
	}
	_, err := newAuthSvc(repo).Register(context.Background(), "exists@example.com", "Strong@1", "", "", "", "", "", "", "")
	require.Error(t, err)
	var de *apperr.DuplicateError
	require.ErrorAs(t, err, &de)
}

// ── Password validation ───────────────────────────────────────────────────────

func TestValidatePassword(t *testing.T) {
	tests := []struct {
		name     string
		password string
		wantErr  bool
	}{
		{"valid", "Strong@1", false},
		{"valid long", "MyP@ssw0rd!IsLong", false},
		{"too short", "Ab@1", true},
		{"no uppercase", "strong@1", true},
		{"no lowercase", "STRONG@1", true},
		{"no digit", "Strong@!", true},
		{"no special char", "Strong12", true},
		{"exactly 8 chars valid", "Aa1!aaaa", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePassword(tt.password)
			if tt.wantErr {
				require.Error(t, err)
				var ve *apperr.ValidationError
				require.ErrorAs(t, err, &ve)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// ── Login ─────────────────────────────────────────────────────────────────────

func TestLogin_Success(t *testing.T) {
	h := hashFor(t, "Strong@1")
	repo := &mockUserRepo{
		getByEmail: func(_ context.Context, email string) (db.User, error) {
			return db.User{ID: uuid.New(), Email: email, HashedPassword: &h, IsActive: true}, nil
		},
	}
	result, err := newAuthSvc(repo).Login(context.Background(), "user@example.com", "Strong@1", false)
	require.NoError(t, err)
	assert.NotEmpty(t, result.AccessToken)
	assert.Equal(t, int64(24*3600), result.ExpiresIn)
}

func TestLogin_RememberMe_Issues90DayToken(t *testing.T) {
	h := hashFor(t, "Strong@1")
	repo := &mockUserRepo{
		getByEmail: func(_ context.Context, email string) (db.User, error) {
			return db.User{ID: uuid.New(), Email: email, HashedPassword: &h, IsActive: true}, nil
		},
	}
	result, err := newAuthSvc(repo).Login(context.Background(), "user@example.com", "Strong@1", true)
	require.NoError(t, err)
	assert.NotEmpty(t, result.AccessToken)
	assert.Equal(t, int64(90*24*3600), result.ExpiresIn)
}

func TestLogin_WrongPassword(t *testing.T) {
	h := hashFor(t, "Correct@1")
	repo := &mockUserRepo{
		getByEmail: func(_ context.Context, email string) (db.User, error) {
			return db.User{ID: uuid.New(), Email: email, HashedPassword: &h, IsActive: true}, nil
		},
	}
	_, err := newAuthSvc(repo).Login(context.Background(), "user@example.com", "Wrong@1", false)
	require.Error(t, err)
	var ve *apperr.ValidationError
	require.ErrorAs(t, err, &ve)
	assert.Contains(t, err.Error(), "invalid email or password")
}

func TestLogin_EmailNotFound(t *testing.T) {
	// Should surface as a generic error, not reveal that email doesn't exist
	_, err := newAuthSvc(&mockUserRepo{}).Login(context.Background(), "nobody@example.com", "Strong@1", false)
	require.Error(t, err)
	var ve *apperr.ValidationError
	require.ErrorAs(t, err, &ve)
	assert.Contains(t, err.Error(), "invalid email or password")
}

func TestLogin_InactiveAccount(t *testing.T) {
	h := hashFor(t, "Strong@1")
	repo := &mockUserRepo{
		getByEmail: func(_ context.Context, email string) (db.User, error) {
			return db.User{ID: uuid.New(), Email: email, HashedPassword: &h, IsActive: false}, nil
		},
	}
	_, err := newAuthSvc(repo).Login(context.Background(), "user@example.com", "Strong@1", false)
	require.Error(t, err)
	var fe *apperr.ForbiddenError
	require.ErrorAs(t, err, &fe)
}

func TestLogin_OAuthOnlyAccount(t *testing.T) {
	repo := &mockUserRepo{
		getByEmail: func(_ context.Context, email string) (db.User, error) {
			return db.User{ID: uuid.New(), Email: email, HashedPassword: nil, IsActive: true}, nil
		},
	}
	_, err := newAuthSvc(repo).Login(context.Background(), "oauth@example.com", "Strong@1", false)
	require.Error(t, err)
	var ve *apperr.ValidationError
	require.ErrorAs(t, err, &ve)
	assert.Contains(t, err.Error(), "OAuth")
}

// ── VerifyEmail ───────────────────────────────────────────────────────────────

func TestVerifyEmail_Success(t *testing.T) {
	userID := uuid.New()
	token := uuid.New()
	var verifiedID uuid.UUID
	repo := &mockUserRepo{
		getByVerification: func(_ context.Context, tok uuid.UUID) (db.User, error) {
			assert.Equal(t, token, tok)
			return db.User{
				ID:                         userID,
				EmailVerificationExpiresAt: pgtype.Timestamptz{Time: time.Now().UTC().Add(5 * time.Minute), Valid: true},
			}, nil
		},
		markVerified: func(_ context.Context, id uuid.UUID) error {
			verifiedID = id
			return nil
		},
	}
	err := newAuthSvc(repo).VerifyEmail(context.Background(), token.String())
	require.NoError(t, err)
	assert.Equal(t, userID, verifiedID)
}

func TestVerifyEmail_MalformedToken(t *testing.T) {
	err := newAuthSvc(&mockUserRepo{}).VerifyEmail(context.Background(), "not-a-uuid")
	require.Error(t, err)
	var ve *apperr.ValidationError
	require.ErrorAs(t, err, &ve)
}

func TestVerifyEmail_UnknownToken(t *testing.T) {
	err := newAuthSvc(&mockUserRepo{}).VerifyEmail(context.Background(), uuid.New().String())
	require.Error(t, err)
	var ve *apperr.ValidationError
	require.ErrorAs(t, err, &ve)
}

func TestVerifyEmail_ExpiredToken(t *testing.T) {
	var markCalled bool
	repo := &mockUserRepo{
		getByVerification: func(_ context.Context, tok uuid.UUID) (db.User, error) {
			return db.User{
				ID:                         uuid.New(),
				EmailVerificationExpiresAt: pgtype.Timestamptz{Time: time.Now().UTC().Add(-time.Minute), Valid: true},
			}, nil
		},
		markVerified: func(_ context.Context, _ uuid.UUID) error {
			markCalled = true
			return nil
		},
	}
	err := newAuthSvc(repo).VerifyEmail(context.Background(), uuid.New().String())
	require.Error(t, err)
	var ve *apperr.ValidationError
	require.ErrorAs(t, err, &ve)
	assert.False(t, markCalled, "expired token must not mark the account verified")
}

// ── ResendVerificationEmail ──────────────────────────────────────────────────

func TestResendVerificationEmail_UnknownEmail_SilentSuccess(t *testing.T) {
	var called bool
	repo := &mockUserRepo{
		setEmailVerification: func(_ context.Context, arg db.SetEmailVerificationTokenParams) (db.User, error) {
			called = true
			return db.User{ID: arg.ID}, nil
		},
	}
	err := newAuthSvc(repo).ResendVerificationEmail(context.Background(), "nobody@example.com")
	require.NoError(t, err)
	assert.False(t, called, "must not mint a token for an unknown email")
}

func TestResendVerificationEmail_AlreadyVerified_NoOp(t *testing.T) {
	var called bool
	repo := &mockUserRepo{
		getByEmail: func(_ context.Context, email string) (db.User, error) {
			return db.User{ID: uuid.New(), Email: email, IsVerified: true}, nil
		},
		setEmailVerification: func(_ context.Context, arg db.SetEmailVerificationTokenParams) (db.User, error) {
			called = true
			return db.User{ID: arg.ID}, nil
		},
	}
	err := newAuthSvc(repo).ResendVerificationEmail(context.Background(), "verified@example.com")
	require.NoError(t, err)
	assert.False(t, called)
}

func TestResendVerificationEmail_Cooldown(t *testing.T) {
	repo := &mockUserRepo{
		getByEmail: func(_ context.Context, email string) (db.User, error) {
			return db.User{
				ID:                          uuid.New(),
				Email:                       email,
				EmailVerificationLastSentAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
			}, nil
		},
	}
	err := newAuthSvc(repo).ResendVerificationEmail(context.Background(), "user@example.com")
	require.Error(t, err)
	var ve *apperr.ValidationError
	require.ErrorAs(t, err, &ve)
}

func TestResendVerificationEmail_Success(t *testing.T) {
	userID := uuid.New()
	var called bool
	repo := &mockUserRepo{
		getByEmail: func(_ context.Context, email string) (db.User, error) {
			return db.User{ID: userID, Email: email}, nil
		},
		setEmailVerification: func(_ context.Context, arg db.SetEmailVerificationTokenParams) (db.User, error) {
			called = true
			assert.Equal(t, userID, arg.ID)
			return db.User{ID: arg.ID}, nil
		},
	}
	err := newAuthSvc(repo).ResendVerificationEmail(context.Background(), "user@example.com")
	require.NoError(t, err)
	assert.True(t, called)
}

// ── UserService ───────────────────────────────────────────────────────────────

func TestUserService_Update_PassesNewFields(t *testing.T) {
	id := uuid.New()
	cc := "US"
	sc := "CA"
	var captured db.UpdateUserParams
	repo := &mockUserRepo{
		update: func(_ context.Context, arg db.UpdateUserParams) (db.User, error) {
			captured = arg
			return db.User{}, nil
		},
	}
	svc := NewUserService(repo, &mockAppleAuth{}, "", &config.Config{}, zap.NewNop())
	_, err := svc.Update(context.Background(), id, UpdateUserInput{
		CountryCode:         &cc,
		StateCode:           &sc,
		FilingStatus:        "1",
		TaxPaymentFrequency: 3,
	})
	require.NoError(t, err)
	assert.Equal(t, &cc, captured.CountryCode)
	assert.Equal(t, &sc, captured.StateCode)
	assert.Equal(t, "1", captured.FilingStatus)
	assert.Equal(t, int32(3), captured.TaxPaymentFrequency)
}

func TestUserService_ListCountries_MergesFeatures(t *testing.T) {
	repo := &mockUserRepo{
		listEnabledCountries: func(_ context.Context) ([]db.ListEnabledCountriesRow, error) {
			return []db.ListEnabledCountriesRow{
				{Code: "US", Name: "United States", IsEnabled: true},
				{Code: "ES", Name: "Spain", IsEnabled: true},
			}, nil
		},
		listCountryFeatures: func(_ context.Context) ([]db.CountryFeature, error) {
			return []db.CountryFeature{
				{CountryCode: "US", FeatureName: "before_tax_income", IsEnabled: true},
			}, nil
		},
	}
	svc := NewUserService(repo, &mockAppleAuth{}, "", &config.Config{}, zap.NewNop())
	countries, byCode, err := svc.ListCountries(context.Background())
	require.NoError(t, err)
	assert.Len(t, countries, 2)
	assert.Len(t, byCode["US"], 1)
	assert.Equal(t, "before_tax_income", byCode["US"][0].FeatureName)
	assert.Empty(t, byCode["ES"])
}

// ── Sign in with Apple ────────────────────────────────────────────────────────

func TestAppleSignIn_NewUser_CreatesAndAutoVerifies(t *testing.T) {
	var created db.CreateUserParams
	var verified uuid.UUID
	repo := &mockUserRepo{
		create: func(_ context.Context, arg db.CreateUserParams) (db.User, error) {
			created = arg
			return db.User{ID: uuid.New(), Email: arg.Email, IsActive: true, Language: arg.Language, Currency: arg.Currency}, nil
		},
		markVerified: func(_ context.Context, id uuid.UUID) error {
			verified = id
			return nil
		},
	}
	apple := &mockAppleAuth{
		verify: func(context.Context, string) (auth.AppleIdentity, error) {
			return auth.AppleIdentity{Sub: "sub-1", Email: "new@example.com", EmailVerified: true}, nil
		},
	}

	result, err := newAuthSvcWithApple(repo, apple).AppleSignIn(context.Background(), "tok", "code", "Jane", "Doe", "es", "EUR")
	require.NoError(t, err)
	assert.NotEmpty(t, result.AccessToken)
	assert.True(t, result.IsNewUser)
	assert.Equal(t, "es", result.Language)
	assert.Equal(t, "EUR", result.Currency)
	// Apple only ever sends the name on the first authorization, so it has to
	// be persisted here or it's lost for good.
	require.NotNil(t, created.FirstName)
	assert.Equal(t, "Jane", *created.FirstName)
	require.NotNil(t, created.LastName)
	assert.Equal(t, "Doe", *created.LastName)
	assert.NotEqual(t, uuid.Nil, verified, "Apple vouched for the address, so the account should skip email verification")
}

// The headline rule from issue #33: an existing account (e.g. created via
// Google) with the same address must be signed into, not duplicated.
func TestAppleSignIn_ExistingEmail_LinksToSameAccount(t *testing.T) {
	existingID := uuid.New()
	var linked db.CreateOAuthAccountParams
	repo := &mockUserRepo{
		getByEmail: func(_ context.Context, email string) (db.User, error) {
			return db.User{ID: existingID, Email: email, IsActive: true, Language: "en", Currency: "USD"}, nil
		},
		create: func(context.Context, db.CreateUserParams) (db.User, error) {
			t.Fatal("must not create a second user for an address that already has an account")
			return db.User{}, nil
		},
		createOAuth: func(_ context.Context, arg db.CreateOAuthAccountParams) (db.OauthAccount, error) {
			linked = arg
			return db.OauthAccount{ID: uuid.New(), UserID: arg.UserID}, nil
		},
	}

	result, err := newAuthSvc(repo).AppleSignIn(context.Background(), "tok", "code", "", "", "", "")
	require.NoError(t, err)
	assert.False(t, result.IsNewUser)
	assert.Equal(t, existingID, linked.UserID)
	assert.Equal(t, "apple", linked.OauthName)
}

func TestAppleSignIn_ReturningUser_ResolvedBySubject(t *testing.T) {
	userID := uuid.New()
	repo := &mockUserRepo{
		getOAuth: func(_ context.Context, arg db.GetOAuthAccountParams) (db.OauthAccount, error) {
			assert.Equal(t, "apple", arg.OauthName)
			assert.Equal(t, "sub-1", arg.AccountID)
			return db.OauthAccount{ID: uuid.New(), UserID: userID}, nil
		},
		getByID: func(_ context.Context, id uuid.UUID) (db.User, error) {
			return db.User{ID: id, IsActive: true, Language: "es", Currency: "EUR"}, nil
		},
		getByEmail: func(context.Context, string) (db.User, error) {
			t.Fatal("a known subject must not fall through to an email lookup")
			return db.User{}, nil
		},
	}
	apple := &mockAppleAuth{
		verify: func(context.Context, string) (auth.AppleIdentity, error) {
			return auth.AppleIdentity{Sub: "sub-1", Email: "known@example.com", EmailVerified: true}, nil
		},
	}

	result, err := newAuthSvcWithApple(repo, apple).AppleSignIn(context.Background(), "tok", "", "", "", "", "")
	require.NoError(t, err)
	assert.False(t, result.IsNewUser)
	assert.Equal(t, "es", result.Language)
}

func TestAppleSignIn_InvalidToken(t *testing.T) {
	apple := &mockAppleAuth{
		verify: func(context.Context, string) (auth.AppleIdentity, error) {
			return auth.AppleIdentity{}, errors.New("signature mismatch")
		},
	}
	_, err := newAuthSvcWithApple(&mockUserRepo{}, apple).AppleSignIn(context.Background(), "bad", "", "", "", "", "")
	require.Error(t, err)
	var ve *apperr.ValidationError
	require.ErrorAs(t, err, &ve)
}

// Adopting a pre-existing account on the strength of an unverified email
// claim would be an account-takeover vector.
func TestAppleSignIn_UnverifiedEmail_RefusesToLinkExistingAccount(t *testing.T) {
	repo := &mockUserRepo{
		getByEmail: func(_ context.Context, email string) (db.User, error) {
			return db.User{ID: uuid.New(), Email: email, IsActive: true}, nil
		},
		createOAuth: func(context.Context, db.CreateOAuthAccountParams) (db.OauthAccount, error) {
			t.Fatal("must not link an unverified claim to an existing account")
			return db.OauthAccount{}, nil
		},
	}
	apple := &mockAppleAuth{
		verify: func(context.Context, string) (auth.AppleIdentity, error) {
			return auth.AppleIdentity{Sub: "sub-1", Email: "victim@example.com", EmailVerified: false}, nil
		},
	}

	_, err := newAuthSvcWithApple(repo, apple).AppleSignIn(context.Background(), "tok", "", "", "", "", "")
	require.Error(t, err)
	var ve *apperr.ValidationError
	require.ErrorAs(t, err, &ve)
}

// An unverified claim for an address nobody owns is harmless — there is no
// account to take over, so a fresh one is created as normal.
func TestAppleSignIn_UnverifiedEmail_StillCreatesBrandNewAccount(t *testing.T) {
	apple := &mockAppleAuth{
		verify: func(context.Context, string) (auth.AppleIdentity, error) {
			return auth.AppleIdentity{Sub: "sub-1", Email: "fresh@example.com", EmailVerified: false}, nil
		},
	}
	result, err := newAuthSvcWithApple(&mockUserRepo{}, apple).AppleSignIn(context.Background(), "tok", "", "", "", "", "")
	require.NoError(t, err)
	assert.True(t, result.IsNewUser)
}

func TestAppleSignIn_StoresEncryptedRefreshToken(t *testing.T) {
	var stored db.UpdateOAuthAccountRefreshTokenParams
	repo := &mockUserRepo{
		updateOAuthRefresh: func(_ context.Context, arg db.UpdateOAuthAccountRefreshTokenParams) error {
			stored = arg
			return nil
		},
	}
	apple := &mockAppleAuth{
		exchange: func(_ context.Context, code string) (string, error) {
			assert.Equal(t, "the-code", code)
			return "apple-refresh-token", nil
		},
	}

	_, err := newAuthSvcWithApple(repo, apple).AppleSignIn(context.Background(), "tok", "the-code", "", "", "", "")
	require.NoError(t, err)
	require.NotNil(t, stored.RefreshToken)
	assert.NotEqual(t, "apple-refresh-token", *stored.RefreshToken, "refresh token must not be stored in plaintext")

	decrypted, err := crypto.Decrypt(*stored.RefreshToken, strings.Repeat("ab", 32))
	require.NoError(t, err)
	assert.Equal(t, "apple-refresh-token", decrypted)
}

// Revocation is a nicety; being able to sign in is not. A failed exchange
// must never block the session.
func TestAppleSignIn_ExchangeFailure_DoesNotFailSignIn(t *testing.T) {
	apple := &mockAppleAuth{
		exchange: func(context.Context, string) (string, error) {
			return "", errors.New("apple is down")
		},
	}
	result, err := newAuthSvcWithApple(&mockUserRepo{}, apple).AppleSignIn(context.Background(), "tok", "code", "", "", "", "")
	require.NoError(t, err)
	assert.NotEmpty(t, result.AccessToken)
}

func TestAppleSignIn_MissingSigningKey_DoesNotFailSignIn(t *testing.T) {
	apple := &mockAppleAuth{
		exchange: func(context.Context, string) (string, error) {
			return "", auth.ErrAppleKeyNotConfigured
		},
	}
	result, err := newAuthSvcWithApple(&mockUserRepo{}, apple).AppleSignIn(context.Background(), "tok", "code", "", "", "", "")
	require.NoError(t, err)
	assert.NotEmpty(t, result.AccessToken)
}

func TestAppleSignIn_InactiveAccount(t *testing.T) {
	repo := &mockUserRepo{
		getOAuth: func(context.Context, db.GetOAuthAccountParams) (db.OauthAccount, error) {
			return db.OauthAccount{ID: uuid.New(), UserID: uuid.New()}, nil
		},
		getByID: func(_ context.Context, id uuid.UUID) (db.User, error) {
			return db.User{ID: id, IsActive: false, Status: "disabled"}, nil
		},
	}
	_, err := newAuthSvc(repo).AppleSignIn(context.Background(), "tok", "", "", "", "", "")
	require.Error(t, err)
	var fe *apperr.ForbiddenError
	require.ErrorAs(t, err, &fe)
}

// ── Account deletion revokes Apple credentials ────────────────────────────────

func newUserSvcWithApple(repo *mockUserRepo, apple auth.AppleAuthenticator) *UserService {
	return NewUserService(repo, apple, strings.Repeat("ab", 32), &config.Config{}, zap.NewNop())
}

func encryptedRefreshToken(t *testing.T, plain string) *string {
	t.Helper()
	enc, err := crypto.Encrypt(plain, strings.Repeat("ab", 32))
	require.NoError(t, err)
	return &enc
}

func TestDeleteUser_RevokesAppleRefreshToken(t *testing.T) {
	userID := uuid.New()
	var revoked string
	repo := &mockUserRepo{
		listOAuthByUser: func(context.Context, uuid.UUID) ([]db.OauthAccount, error) {
			return []db.OauthAccount{
				{OauthName: "google", AccountID: "g-1"},
				{OauthName: "apple", AccountID: "a-1", RefreshToken: encryptedRefreshToken(t, "apple-refresh")},
			}, nil
		},
	}
	apple := &mockAppleAuth{
		revoke: func(_ context.Context, token string) error {
			revoked = token
			return nil
		},
	}

	require.NoError(t, newUserSvcWithApple(repo, apple).Delete(context.Background(), userID))
	assert.Equal(t, "apple-refresh", revoked, "the stored token must be decrypted before being sent to Apple")
}

// A user who asked to be deleted must end up deleted even if Apple is
// unreachable.
func TestDeleteUser_RevocationFailure_StillDeletes(t *testing.T) {
	deleted := false
	repo := &mockUserRepo{
		listOAuthByUser: func(context.Context, uuid.UUID) ([]db.OauthAccount, error) {
			return []db.OauthAccount{{OauthName: "apple", RefreshToken: encryptedRefreshToken(t, "apple-refresh")}}, nil
		},
		softDelete: func(context.Context, uuid.UUID) error {
			deleted = true
			return nil
		},
	}
	apple := &mockAppleAuth{
		revoke: func(context.Context, string) error { return errors.New("apple is down") },
	}

	require.NoError(t, newUserSvcWithApple(repo, apple).Delete(context.Background(), uuid.New()))
	assert.True(t, deleted, "the account must still be soft-deleted when Apple's revoke call fails")
}

func TestDeleteUser_NoAppleAccount_SkipsRevocation(t *testing.T) {
	repo := &mockUserRepo{
		listOAuthByUser: func(context.Context, uuid.UUID) ([]db.OauthAccount, error) {
			return []db.OauthAccount{{OauthName: "google", AccountID: "g-1"}}, nil
		},
	}
	apple := &mockAppleAuth{
		revoke: func(context.Context, string) error {
			t.Fatal("must not call Apple for a user with no Apple account")
			return nil
		},
	}
	require.NoError(t, newUserSvcWithApple(repo, apple).Delete(context.Background(), uuid.New()))
}

// ── ChangeEmail ───────────────────────────────────────────────────────────────

func newUserSvc(repo *mockUserRepo) *UserService {
	return NewUserService(repo, &mockAppleAuth{}, "", &config.Config{}, zap.NewNop())
}

func TestChangeEmail_Success_NormalizesAndResetsVerification(t *testing.T) {
	userID := uuid.New()
	var wrote db.UpdateUserEmailParams
	var tokenSetFor uuid.UUID
	repo := &mockUserRepo{
		getByID: func(context.Context, uuid.UUID) (db.User, error) {
			return db.User{ID: userID, Email: "typo@example.com", IsVerified: false}, nil
		},
		getByEmail: func(_ context.Context, email string) (db.User, error) {
			return db.User{}, apperr.NotFound("user", email)
		},
		updateEmail: func(_ context.Context, arg db.UpdateUserEmailParams) (db.User, error) {
			wrote = arg
			return db.User{ID: arg.ID, Email: arg.Email, IsVerified: false}, nil
		},
		setEmailVerification: func(_ context.Context, arg db.SetEmailVerificationTokenParams) (db.User, error) {
			tokenSetFor = arg.ID
			return db.User{}, nil
		},
	}

	user, err := newUserSvc(repo).ChangeEmail(context.Background(), userID, "  Correct@Example.COM ")

	require.NoError(t, err)
	assert.Equal(t, "correct@example.com", wrote.Email, "the address must be lowercased and trimmed before storage")
	assert.Equal(t, "correct@example.com", user.Email)
	assert.False(t, user.IsVerified, "the new address is unproven, so verification must re-open")
	assert.Equal(t, userID, tokenSetFor, "a fresh verification link must be sent to the new address")
}

// A send failure must not roll the change back — the address is already
// corrected, and the user can fall back to resend, which now reaches them.
func TestChangeEmail_SendFailureStillChangesTheAddress(t *testing.T) {
	userID := uuid.New()
	repo := &mockUserRepo{
		getByID: func(context.Context, uuid.UUID) (db.User, error) {
			return db.User{ID: userID, Email: "old@example.com"}, nil
		},
		setEmailVerification: func(context.Context, db.SetEmailVerificationTokenParams) (db.User, error) {
			return db.User{}, errors.New("resend is down")
		},
	}

	user, err := newUserSvc(repo).ChangeEmail(context.Background(), userID, "new@example.com")

	require.NoError(t, err)
	assert.Equal(t, "new@example.com", user.Email)
}

func TestChangeEmail_RejectsAddressTakenByAnotherAccount(t *testing.T) {
	userID := uuid.New()
	repo := &mockUserRepo{
		getByID: func(context.Context, uuid.UUID) (db.User, error) {
			return db.User{ID: userID, Email: "mine@example.com"}, nil
		},
		getByEmail: func(context.Context, string) (db.User, error) {
			return db.User{ID: uuid.New(), Email: "taken@example.com"}, nil
		},
		updateEmail: func(context.Context, db.UpdateUserEmailParams) (db.User, error) {
			t.Fatal("must not write an address that belongs to another account")
			return db.User{}, nil
		},
	}

	_, err := newUserSvc(repo).ChangeEmail(context.Background(), userID, "taken@example.com")

	require.Error(t, err)
	var dup *apperr.DuplicateError
	require.ErrorAs(t, err, &dup)
}

// Succeeding silently here would read as the feature being broken: the caller
// is staring at a verification wall waiting for mail that never comes.
func TestChangeEmail_RejectsTheAddressAlreadyOnTheAccount(t *testing.T) {
	userID := uuid.New()
	repo := &mockUserRepo{
		getByID: func(context.Context, uuid.UUID) (db.User, error) {
			return db.User{ID: userID, Email: "same@example.com"}, nil
		},
		updateEmail: func(context.Context, db.UpdateUserEmailParams) (db.User, error) {
			t.Fatal("must not rewrite the address to itself")
			return db.User{}, nil
		},
	}

	// Also proves the comparison happens after normalization.
	_, err := newUserSvc(repo).ChangeEmail(context.Background(), userID, "SAME@example.com")

	require.Error(t, err)
	var invalid *apperr.ValidationError
	require.ErrorAs(t, err, &invalid)
}

func TestChangeEmail_RejectsMalformedAddress(t *testing.T) {
	for _, email := range []string{"", "   ", "not-an-email", "@example.com", "user@"} {
		t.Run(email, func(t *testing.T) {
			repo := &mockUserRepo{
				getByID: func(context.Context, uuid.UUID) (db.User, error) {
					t.Fatal("must reject before touching the database")
					return db.User{}, nil
				},
			}

			_, err := newUserSvc(repo).ChangeEmail(context.Background(), uuid.New(), email)

			require.Error(t, err)
			var invalid *apperr.ValidationError
			require.ErrorAs(t, err, &invalid)
		})
	}
}
