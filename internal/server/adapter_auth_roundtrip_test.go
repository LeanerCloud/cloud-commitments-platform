package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha1" // #nosec G505 -- RFC 6238 TOTP is defined over HMAC-SHA1; test helper only
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/api"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
)

// Round-trip tests for issue #1907 (pairs U1, U3-U10): each request field is
// asserted where it reaches the auth store, through the real router, the api
// handler, authServiceAdapter and the real *auth.Service.

const (
	rtUserID     = "user-rt"
	rtEmail      = "rt-user@example.com"
	rtPassword   = "RoundTripPass@1"    // test-only placeholder
	rtNewSecret  = "RoundTripNew@Pass9" // test-only placeholder
	rtMFASecret  = "JBSWY3DPEHPK3PXP"
	rtSessionTok = "rt-session-token"
)

// rtTOTP computes the RFC 6238 code for the current 30-second step.
func rtTOTP(t *testing.T, secret string) (code string, counter int64) {
	t.Helper()
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	require.NoError(t, err)
	counter = time.Now().Unix() / 30
	msg := make([]byte, 8)
	binary.BigEndian.PutUint64(msg, uint64(counter)) // #nosec G115 -- counter is positive
	mac := hmac.New(sha1.New, key)
	mac.Write(msg)
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := (binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff) % 1000000
	return fmt.Sprintf("%06d", v), counter
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

type rtEmailSender struct{ to, resetURL string }

func (e *rtEmailSender) SendPasswordResetEmail(_ context.Context, to, resetURL string) error {
	e.to, e.resetURL = to, resetURL
	return nil
}

func (e *rtEmailSender) SendUserInviteEmail(context.Context, string, string) error { return nil }

type authRT struct {
	t      *testing.T
	mail   *rtEmailSender
	store  *auth.MockStore
	router *api.Router
	user   *auth.User
}

func newAuthRT(t *testing.T) *authRT {
	t.Helper()
	store := new(auth.MockStore)
	mail := new(rtEmailSender)
	svc := auth.NewService(auth.ServiceConfig{
		EmailSender: mail, Store: store, SessionDuration: time.Hour, CSRFKey: auth.TestCSRFKey(),
		DashboardURL: "http://localhost:3000", BcryptCost: bcrypt.MinCost,
	})
	router := api.NewRouter(api.NewHandler(api.HandlerConfig{
		AuthService: newAuthServiceAdapter(svc), RateLimiter: api.NewInMemoryRateLimiter(),
	}))
	h := &authRT{t: t, mail: mail, store: store, router: router, user: &auth.User{
		ID: rtUserID, Email: rtEmail, Active: true, GroupIDs: []string{"group-rt"},
		PasswordHash: auth.TestPasswordHash(t, rtPassword),
	}}
	t.Cleanup(func() { store.AssertExpectations(t) })
	return h
}

// session stubs the session lookup for authenticated routes.
func (a *authRT) session() {
	a.store.On("GetSession", mock.Anything, hashSessionTokenForTest(rtSessionTok)).Return(&auth.Session{
		Token: hashSessionTokenForTest(rtSessionTok), UserID: rtUserID, Email: rtEmail, ExpiresAt: time.Now().Add(time.Hour),
	}, nil)
	a.store.On("GetUserByID", mock.Anything, rtUserID).Return(a.user, nil)
}

// bestEffortCredentialRevocation stubs the session and API-key cleanup that
// follows every password change.
func (a *authRT) bestEffortCredentialRevocation() {
	a.store.On("DeleteUserSessions", mock.Anything, rtUserID).Return(nil)
	a.store.On("ListAPIKeysByUser", mock.Anything, rtUserID).Return(nil, nil)
}

func (a *authRT) post(method, path string, authed bool, body string) (any, error) {
	req := &events.LambdaFunctionURLRequest{Body: body, Headers: map[string]string{}}
	req.RequestContext.HTTP.SourceIP = "127.0.0.1"
	if authed {
		req.Headers["authorization"] = "Bearer " + rtSessionTok
	}
	return a.router.Route(context.Background(), method, path, req)
}

func (a *authRT) requireHashOf(hash, password string) {
	a.t.Helper()
	require.NoError(a.t, bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)))
}

func TestLogin_RoundTripEveryField(t *testing.T) {
	t.Run("email and password", func(t *testing.T) {
		a := newAuthRT(t)
		a.store.On("GetUserByEmail", mock.Anything, rtEmail).Return(a.user, nil).Once()
		var session *auth.Session
		a.store.On("CreateSession", mock.Anything, mock.AnythingOfType("*auth.Session")).
			Run(func(args mock.Arguments) { session = args.Get(1).(*auth.Session) }).Return(nil).Once()
		a.store.On("RecordSuccessfulLogin", mock.Anything, rtUserID).Return(nil).Once()

		res, err := a.post("POST", "/api/auth/login", false, `{"email":"`+rtEmail+`","password":"`+b64(rtPassword)+`"}`)
		require.NoError(t, err)
		resp := res.(*api.LoginResponse)
		require.NotNil(t, session)
		assert.Equal(t, rtUserID, session.UserID)
		assert.NotEmpty(t, resp.Token)
		assert.NotEmpty(t, resp.CSRFToken)
		_, perr := time.Parse(time.RFC3339, resp.ExpiresAt)
		assert.NoError(t, perr)
		assert.Equal(t, &api.UserInfo{ID: rtUserID, Email: rtEmail, Groups: []string{"group-rt"}}, resp.User)
	})

	t.Run("mfa_code with password", func(t *testing.T) {
		a := newAuthRT(t)
		a.user.MFAEnabled, a.user.MFASecret = true, rtMFASecret
		code, counter := rtTOTP(t, rtMFASecret)
		a.store.On("GetUserByEmail", mock.Anything, rtEmail).Return(a.user, nil).Once()
		a.store.On("ClaimTOTPCounter", mock.Anything, rtUserID, counter).Return(true, nil).Once()
		a.store.On("CreateSession", mock.Anything, mock.AnythingOfType("*auth.Session")).Return(nil).Once()
		a.store.On("RecordSuccessfulLogin", mock.Anything, rtUserID).Return(nil).Once()

		res, err := a.post("POST", "/api/auth/login", false,
			`{"email":"`+rtEmail+`","password":"`+b64(rtPassword)+`","mfa_code":"`+code+`"}`)
		require.NoError(t, err)
		assert.True(t, res.(*api.LoginResponse).User.MFAEnabled)
	})
}

func TestForgotPassword_RoundTripEmail(t *testing.T) {
	a := newAuthRT(t)
	a.store.On("GetUserByEmail", mock.Anything, rtEmail).Return(a.user, nil).Once()
	var stored *auth.User
	a.store.On("SetPasswordResetToken", mock.Anything, mock.AnythingOfType("*auth.User"), mock.Anything).
		Run(func(args mock.Arguments) { stored = args.Get(1).(*auth.User) }).Return(nil).Once()

	_, err := a.post("POST", "/api/auth/forgot-password", false, `{"email":"`+rtEmail+`"}`)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, rtUserID, stored.ID, "the account named by email gets the reset token")
	assert.Equal(t, rtEmail, a.mail.to, "the reset link goes to the requested address")
	token := strings.TrimPrefix(a.mail.resetURL, "http://localhost:3000/reset-password?token=")
	assert.Equal(t, hashSessionTokenForTest(token), stored.PasswordResetToken, "only the emailed token's hash is stored")
}

func TestResetPassword_RoundTripEveryField(t *testing.T) {
	a := newAuthRT(t)
	const token = "reset-token-rt"
	a.user.PasswordResetToken = hashSessionTokenForTest(token)
	expiry := time.Now().Add(time.Hour)
	a.user.PasswordResetExpiry = &expiry
	oldHash := a.user.PasswordHash
	// token reaches the lookup as its hash.
	a.store.On("GetUserByResetToken", mock.Anything, hashSessionTokenForTest(token)).Return(a.user, nil).Once()
	var saved *auth.User
	a.store.On("CompletePasswordReset", mock.Anything, mock.AnythingOfType("*auth.User"), hashSessionTokenForTest(token), oldHash).
		Run(func(args mock.Arguments) { saved = args.Get(1).(*auth.User) }).Return(nil).Once()
	a.bestEffortCredentialRevocation()

	_, err := a.post("POST", "/api/auth/reset-password", false,
		`{"token":"`+token+`","new_password":"`+b64(rtNewSecret)+`"}`)
	require.NoError(t, err)
	require.NotNil(t, saved)
	a.requireHashOf(saved.PasswordHash, rtNewSecret) // base64-decoded new_password is what is hashed
}

func TestUpdateProfile_RoundTripEveryField(t *testing.T) {
	a := newAuthRT(t)
	a.session()
	const newEmail = "renamed@example.com"
	oldHash := a.user.PasswordHash
	a.store.On("GetUserByEmail", mock.Anything, newEmail).Return(nil, nil).Once()
	var saved *auth.User
	a.store.On("UpdateUserCredentials", mock.Anything, mock.AnythingOfType("*auth.User"), rtEmail, oldHash).
		Run(func(args mock.Arguments) { saved = args.Get(1).(*auth.User) }).Return(nil).Once()
	a.bestEffortCredentialRevocation()

	_, err := a.post("PUT", "/api/auth/profile", true,
		`{"email":"`+newEmail+`","current_password":"`+b64(rtPassword)+`","new_password":"`+b64(rtNewSecret)+`"}`)
	require.NoError(t, err)
	require.NotNil(t, saved)
	assert.Equal(t, rtUserID, saved.ID, "the session user, not a body field, is updated")
	assert.Equal(t, newEmail, saved.Email)
	a.requireHashOf(saved.PasswordHash, rtNewSecret)
}

func TestChangePassword_RoundTripEveryField(t *testing.T) {
	a := newAuthRT(t)
	a.session()
	oldHash := a.user.PasswordHash
	var saved *auth.User
	a.store.On("UpdateUserCredentials", mock.Anything, mock.AnythingOfType("*auth.User"), rtEmail, oldHash).
		Run(func(args mock.Arguments) { saved = args.Get(1).(*auth.User) }).Return(nil).Once()
	a.bestEffortCredentialRevocation()

	_, err := a.post("POST", "/api/auth/change-password", true,
		`{"current_password":"`+b64(rtPassword)+`","new_password":"`+b64(rtNewSecret)+`"}`)
	require.NoError(t, err)
	require.NotNil(t, saved)
	assert.Equal(t, rtUserID, saved.ID)
	a.requireHashOf(saved.PasswordHash, rtNewSecret)
	assert.Equal(t, []string{oldHash}, saved.PasswordHistory, "the verified current password moves into history")
}

func TestMFASetup_RoundTripPassword(t *testing.T) {
	a := newAuthRT(t)
	a.session()
	var storedSecret string
	a.store.On("SetPendingMFASecret", mock.Anything, rtUserID, mock.AnythingOfType("string"), mock.Anything).
		Run(func(args mock.Arguments) { storedSecret = args.String(2) }).Return(nil).Once()

	res, err := a.post("POST", "/api/auth/mfa/setup", true, `{"password":"`+b64(rtPassword)+`"}`)
	require.NoError(t, err)
	resp := res.(*api.MFASetupResponse)
	assert.Equal(t, storedSecret, resp.Secret, "the stored pending secret is the one returned")
	assert.Contains(t, resp.ProvisioningURI, storedSecret)
}

func TestMFAEnable_RoundTripCode(t *testing.T) {
	a := newAuthRT(t)
	a.session()
	pendingExpiry := time.Now().Add(time.Hour)
	a.user.MFAPendingSecret, a.user.MFAPendingSecretExpiresAt = rtMFASecret, &pendingExpiry
	code, counter := rtTOTP(t, rtMFASecret)
	var hashes []string
	a.store.On("EnableMFA", mock.Anything, rtUserID, rtMFASecret, counter, mock.Anything).
		Run(func(args mock.Arguments) { hashes = args.Get(4).([]string) }).Return(nil).Once()

	res, err := a.post("POST", "/api/auth/mfa/enable", true, `{"code":"`+code+`"}`)
	require.NoError(t, err)
	codes := res.(*api.MFAEnableResponse).RecoveryCodes
	require.NotEmpty(t, codes)
	require.Len(t, hashes, len(codes))
	for i, c := range codes {
		a.requireHashOf(hashes[i], normalizeRecoveryForTest(c))
	}
}

func TestMFADisable_RoundTripEveryField(t *testing.T) {
	a := newAuthRT(t)
	a.session()
	const recovery = "ABCD-EFGH"
	recoveryHash := auth.TestPasswordHash(t, "ABCDEFGH")
	a.user.MFAEnabled, a.user.MFASecret, a.user.MFARecoveryCodes = true, rtMFASecret, []string{recoveryHash}
	// password verified, then the recovery code (not a TOTP) is the proof.
	a.store.On("DisableMFA", mock.Anything, rtUserID, rtMFASecret, []string{recoveryHash}).Return(nil).Once()

	res, err := a.post("POST", "/api/auth/mfa/disable", true,
		`{"password":"`+b64(rtPassword)+`","code":"`+recovery+`"}`)
	require.NoError(t, err)
	assert.Equal(t, &api.StatusResponse{Status: "mfa disabled"}, res)
}

func TestMFARegenerate_RoundTripCode(t *testing.T) {
	a := newAuthRT(t)
	a.session()
	oldHashes := []string{"old-hash-1"}
	a.user.MFAEnabled, a.user.MFASecret, a.user.MFARecoveryCodes = true, rtMFASecret, oldHashes
	code, counter := rtTOTP(t, rtMFASecret)
	a.store.On("ClaimTOTPCounter", mock.Anything, rtUserID, counter).Return(true, nil).Once()
	var newHashes []string
	a.store.On("ReplaceMFARecoveryCodes", mock.Anything, rtUserID, rtMFASecret, oldHashes, mock.Anything).
		Run(func(args mock.Arguments) { newHashes = args.Get(4).([]string) }).Return(nil).Once()

	res, err := a.post("POST", "/api/auth/mfa/regenerate-recovery-codes", true, `{"code":"`+code+`"}`)
	require.NoError(t, err)
	codes := res.(*api.MFARegenerateResponse).RecoveryCodes
	require.Len(t, newHashes, len(codes))
	a.requireHashOf(newHashes[0], normalizeRecoveryForTest(codes[0]))
}

func normalizeRecoveryForTest(code string) string {
	out := make([]byte, 0, len(code))
	for i := 0; i < len(code); i++ {
		if code[i] != '-' && code[i] != ' ' {
			out = append(out, code[i])
		}
	}
	return string(out)
}
