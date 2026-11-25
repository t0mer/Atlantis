package tplinkcloud

import (
	"context"
	"errors"
	"fmt"
)

// MFA types (com/tplink/cloud/bean/mfa/MfaType.java).
const (
	MFAPush  = 1
	MFAEmail = 2
	MFATOTP  = 3
	MFAPhone = 4
)

// MFAChallenge is returned (as an error) by Login when the account needs a
// verification code. Complete it with SendMFACode (for email or SMS codes)
// and VerifyMFA.
type MFAChallenge struct {
	ProcessID   string
	Types       []int  // supported MFA types
	MaskedEmail string // where an email code goes, as masked by the cloud
}

func (m *MFAChallenge) Error() string { return "tplinkcloud: two-step verification required" }

type accountStatusResult struct {
	AppServerURL   string `json:"appServerUrl"`
	AppServerURLV2 string `json:"appServerUrlV2"`
	Status         int    `json:"status"`
	LockedMinutes  int    `json:"lockedMinutes"`
}

type loginParams struct {
	AppType            string `json:"appType"`
	AppVersion         string `json:"appVersion"`
	CloudUserName      string `json:"cloudUserName"`
	CloudPassword      string `json:"cloudPassword"`
	Platform           string `json:"platform"`
	RefreshTokenNeeded bool   `json:"refreshTokenNeeded"`
	TerminalMeta       string `json:"terminalMeta"`
	TerminalName       string `json:"terminalName"`
	TerminalUUID       string `json:"terminalUUID"`
}

type loginResult struct {
	Token             string `json:"token"`
	AccountID         string `json:"accountId"`
	Email             string `json:"email"`
	MFAProcessID      string `json:"MFAProcessId"`
	SupportedMFATypes []int  `json:"supportedMFATypes"`
	MFAEmail          string `json:"mfaEmail"`
	RemainAttempts    *int   `json:"remainAttempts"`
	LockedMinutes     int    `json:"lockedMinutes"`
}

// Login signs in with a TP-Link ID. It returns *MFAChallenge when a
// verification code is needed, and an error matching ErrAuthFailed when the
// credentials are rejected; neither should be retried automatically.
func (c *Client) Login(ctx context.Context, email, password string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.loginLocked(ctx, email, password)
}

func (c *Client) loginLocked(ctx context.Context, email, password string) error {
	c.token, c.mfa = "", nil
	if c.accountURL == "" || c.email != email {
		var st accountStatusResult
		err := c.post(ctx, c.opts.BootstrapURL, "/api/v2/account/getAccountStatusAndUrl",
			map[string]string{"cloudUserName": email, "appType": AppType}, &st, "")
		if err != nil {
			return fmt.Errorf("looking up account region: %w", err)
		}
		c.accountURL = st.AppServerURLV2
		if c.accountURL == "" {
			c.accountURL = st.AppServerURL
		}
		if c.accountURL == "" {
			return errors.New("tplinkcloud: cloud did not return an account server URL")
		}
	}
	c.email, c.password = email, password

	var res loginResult
	err := c.post(ctx, c.accountURL, "/api/v2/account/login", loginParams{
		AppType:       AppType,
		AppVersion:    c.opts.AppVersion,
		CloudUserName: email,
		CloudPassword: password,
		Platform:      "Android 14",
		TerminalMeta:  "1",
		TerminalName:  "Atlantis",
		TerminalUUID:  c.opts.TerminalUUID,
	}, &res, "")
	return c.finishLogin(res, err)
}

func (c *Client) finishLogin(res loginResult, err error) error {
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Code == CodeMFARequired {
		c.mfa = &MFAChallenge{ProcessID: res.MFAProcessID, Types: res.SupportedMFATypes, MaskedEmail: res.MFAEmail}
		return c.mfa
	}
	if err != nil {
		if errors.Is(err, ErrAuthFailed) {
			c.password = "" // never reuse rejected credentials
			if res.RemainAttempts != nil {
				return fmt.Errorf("%w (%d attempts left)", err, *res.RemainAttempts)
			}
		}
		return err
	}
	if res.Token == "" {
		return errors.New("tplinkcloud: login returned no token")
	}
	c.token, c.accountID = res.Token, res.AccountID
	return nil
}

// SendMFACode asks the cloud to send a verification code by email
// (MFAEmail) or SMS (MFAPhone). TOTP and push need no request.
func (c *Client) SendMFACode(ctx context.Context, mfaType int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.mfa == nil {
		return errors.New("tplinkcloud: no pending two-step verification")
	}
	var path string
	switch mfaType {
	case MFAEmail:
		path = "/api/v2/account/getEmailVC4TerminalMFA"
	case MFAPhone:
		path = "/api/v2/account/getSmsVC4TerminalMFA"
	default:
		return nil
	}
	return c.post(ctx, c.accountURL, path, map[string]string{
		"appType":       AppType,
		"cloudUserName": c.email,
		"cloudPassword": c.password,
		"locale":        c.opts.Locale,
		"terminalUUID":  c.opts.TerminalUUID,
	}, nil, "")
}

// VerifyMFA completes a pending two-step verification with code.
func (c *Client) VerifyMFA(ctx context.Context, mfaType int, code string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.mfa == nil {
		return errors.New("tplinkcloud: no pending two-step verification")
	}
	var res loginResult
	err := c.post(ctx, c.accountURL, "/api/v2/account/checkMFACodeAndLogin", map[string]any{
		"appType":             AppType,
		"cloudUserName":       c.email,
		"code":                code,
		"MFAProcessId":        c.mfa.ProcessID,
		"MFAType":             mfaType,
		"terminalBindEnabled": true,
	}, &res, "")
	if err == nil {
		c.mfa = nil
	}
	return c.finishLogin(res, err)
}

// call makes an authenticated account call. On an expired token it logs in
// again once with the stored credentials (as the app does) and retries.
// The lock guards session state only, so calls for different sites run in
// parallel.
func (c *Client) call(ctx context.Context, base, path string, body, out any) error {
	token := c.Token()
	if token == "" {
		return ErrNotLoggedIn
	}
	err := c.post(ctx, base, path, body, out, token)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != CodeTokenExpired {
		return err
	}
	if token, err = c.relogin(ctx, token); err != nil {
		return err
	}
	return c.post(ctx, base, path, body, out, token)
}

// relogin replaces an expired token. If another caller already did, it
// returns the newer token without logging in again.
func (c *Client) relogin(ctx context.Context, expired string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && c.token != expired {
		return c.token, nil
	}
	if c.password == "" {
		return "", fmt.Errorf("token expired: %w", ErrNotLoggedIn)
	}
	if err := c.loginLocked(ctx, c.email, c.password); err != nil {
		return "", fmt.Errorf("token expired, re-login failed: %w", err)
	}
	return c.token, nil
}

// Token returns the current account token, or "" before login. The ATA relay
// embeds it in its inner requests; callers must treat it as a secret.
func (c *Client) Token() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token
}

// Email returns the signed-in TP-Link ID.
func (c *Client) Email() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.email
}
