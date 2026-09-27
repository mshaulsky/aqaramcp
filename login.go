package aqaramcp

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Credentials sign in to the Aqara Agent service in place of a person at
// https://agent.aqara.com/login: the account of the Aqara Home app, the MD5
// of its password and the region the account was registered in.
//
// The service never sees the password itself, only its MD5, and neither does
// this package: it takes the digest, so the password need not exist on the
// machine that signs in. The digest is still a credential — anyone holding
// it can sign in.
type Credentials struct {
	Username    string // the account as typed into the app: e-mail or phone number
	PasswordMD5 string // hex MD5 of the password, in either case
	Region      string // one of the Region constants
}

// Key is what signing in yields: a Bearer key for the MCP server (and the
// REST API on the same host), valid until the service decides otherwise.
type Key struct {
	APIKey string
	Region string // the region the service confirmed
}

// Regions the service knows. The login page picks one from the country of
// the account; a wrong one is refused like a wrong password.
const (
	RegionEU = "EU" // Europe and Africa
	RegionRU = "RU" // Russia, Kazakhstan and the rest of the CIS
	RegionUS = "US" // the Americas
	RegionCN = "CN" // mainland China
	RegionKR = "KR" // Korea
	RegionSG = "SG" // the rest of Asia, Australia and the Middle East
)

// loginPath is the sign-in endpoint, on the same origin as the MCP server.
const loginPath = "/login"

// maxLoginBody bounds the sign-in answer, which is a few hundred bytes.
const maxLoginBody = 64 << 10

// loginRequest is what the login page posts.
type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"` // hex MD5
	Region   string `json:"region"`
}

// loginResponse is the service's answer, in either of its shapes: the
// envelope with a code, or a bare detail for a request it could not read.
type loginResponse struct {
	Code    *int   `json:"code"`
	Message string `json:"message"`
	Result  struct {
		APIKey string `json:"api_key"`
		Region string `json:"region"`
	} `json:"result"`
}

// Login signs in and returns a fresh key, which is what the login page does
// when a person presses the button: one POST with the account, the MD5 of
// the password (printf %s "$password" | md5sum) and the region. Options apply as for [New]: [WithEndpoint]
// picks the server (the sign-in lives on its origin), [WithHTTPClient] the
// transport. A refusal is a [*LoginError].
func Login(ctx context.Context, creds Credentials, opts ...Option) (Key, error) {
	c := &Client{endpoint: DefaultEndpoint, http: &http.Client{Timeout: defaultTimeout}}
	for _, o := range opts {
		o(c)
	}
	key, err := c.login(ctx, creds)
	if err != nil {
		return Key{}, fmt.Errorf("aqaramcp: %w", err)
	}
	return key, nil
}

// validate reports what is missing for a sign-in.
func (creds Credentials) validate() error {
	switch {
	case creds.Username == "":
		return errors.New("login: username is required")
	case creds.PasswordMD5 == "":
		return errors.New("login: password MD5 is required")
	case !isMD5(creds.PasswordMD5):
		return errors.New("login: password MD5 must be 32 hex digits")
	case creds.Region == "":
		return errors.New("login: region is required")
	}
	return nil
}

// digest returns the MD5 the way the service expects it.
func (creds Credentials) digest() string {
	return strings.ToLower(creds.PasswordMD5)
}

// isMD5 reports whether s is a hex MD5 digest.
func isMD5(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 16
}

// login performs the sign-in with the client's transport and endpoint.
func (c *Client) login(ctx context.Context, creds Credentials) (Key, error) {
	if err := creds.validate(); err != nil {
		return Key{}, err
	}
	target, err := loginURL(c.endpoint)
	if err != nil {
		return Key{}, err
	}
	body, err := json.Marshal(loginRequest{Username: creds.Username, Password: creds.digest(), Region: creds.Region})
	if err != nil {
		return Key{}, fmt.Errorf("login: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return Key{}, fmt.Errorf("login: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return Key{}, fmt.Errorf("login: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxLoginBody))
	if err != nil {
		return Key{}, fmt.Errorf("login: read response: %w", err)
	}
	var reply loginResponse
	if err := json.Unmarshal(raw, &reply); err != nil || reply.Code == nil {
		// Not the service's envelope: a proxy, an outage, or a request it
		// could not read, which it reports as a 400 with a detail.
		return Key{}, &HTTPError{StatusCode: resp.StatusCode, Status: resp.Status, Method: "login", Body: truncate(raw)}
	}
	if *reply.Code != 0 {
		return Key{}, &LoginError{Code: *reply.Code, Message: reply.Message}
	}
	if reply.Result.APIKey == "" {
		return Key{}, errors.New("login: the service answered without a key")
	}
	return Key{APIKey: reply.Result.APIKey, Region: reply.Result.Region}, nil
}

// loginURL is the sign-in endpoint on the MCP server's origin.
func loginURL(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("login: endpoint %q has no origin to sign in at", endpoint)
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: loginPath}).String(), nil
}
