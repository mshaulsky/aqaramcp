package aqaramcp

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"go.uber.org/mock/gomock"
)

func TestLogin(t *testing.T) {
	tests := []struct {
		name     string
		server   *server
		creds    Credentials
		broken   bool   // use an HTTP client that always fails
		endpoint string // overrides the fake server's address
		want     Key
		wantSent loginRequest // what the server must have received
		wantErr  string
	}{
		{
			name:     "signs in with the account, the digest and the region",
			server:   &server{},
			creds:    testCreds,
			want:     Key{APIKey: "issued-key-1", Region: RegionRU},
			wantSent: loginRequest{Username: "home@example.com", Password: testDigest, Region: RegionRU},
		},
		{
			name:     "lowercases the digest",
			server:   &server{},
			creds:    Credentials{Username: "home@example.com", PasswordMD5: strings.ToUpper(testDigest), Region: RegionEU},
			want:     Key{APIKey: "issued-key-1", Region: RegionEU},
			wantSent: loginRequest{Username: "home@example.com", Password: testDigest, Region: RegionEU},
		},
		{
			name:     "a refusal carries the service's words",
			server:   &server{loginCode: 1001},
			creds:    testCreds,
			wantSent: loginRequest{Username: "home@example.com", Password: testDigest, Region: RegionRU},
			wantErr:  "aqaramcp: login refused: Sign-in failed. Check your account, password, or region and try again. (code 1001)",
		},
		{
			name:     "an unreadable request is reported as it arrived",
			server:   &server{loginBroken: true},
			creds:    testCreds,
			wantSent: loginRequest{Username: "home@example.com", Password: testDigest, Region: RegionRU},
			wantErr:  `aqaramcp: mcp http login: 400 Bad Request: {"detail":"Failed to process login credentials. Check encryption/key."}`,
		},
		{
			name:    "the username is required",
			server:  &server{},
			creds:   Credentials{PasswordMD5: testDigest, Region: RegionRU},
			wantErr: "aqaramcp: login: username is required",
		},
		{
			name:    "the digest is required",
			server:  &server{},
			creds:   Credentials{Username: "home@example.com", Region: RegionRU},
			wantErr: "aqaramcp: login: password MD5 is required",
		},
		{
			name:    "the password itself is refused",
			server:  &server{},
			creds:   Credentials{Username: "home@example.com", PasswordMD5: "correct horse", Region: RegionRU},
			wantErr: "aqaramcp: login: password MD5 must be 32 hex digits",
		},
		{
			name:    "the region is required",
			server:  &server{},
			creds:   Credentials{Username: "home@example.com", PasswordMD5: testDigest},
			wantErr: "aqaramcp: login: region is required",
		},
		{
			name:    "a transport failure is wrapped",
			server:  &server{},
			creds:   testCreds,
			broken:  true,
			wantErr: "aqaramcp: login: " + errTransport.Error(),
		},
		{
			name:     "an endpoint without an origin cannot sign in",
			server:   &server{},
			creds:    testCreds,
			endpoint: "mcp",
			wantErr:  `aqaramcp: login: endpoint "mcp" has no origin to sign in at`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, s := newServer(t, tt.server)
			opts := []Option{WithEndpoint(c.endpoint)}
			if tt.endpoint != "" {
				opts = []Option{WithEndpoint(tt.endpoint)}
			}
			if tt.broken {
				opts = append(opts, WithHTTPClient(brokenDoer(t)))
			}

			got, err := Login(t.Context(), tt.creds, opts...)
			if sent := s.signIns(); (len(sent) == 0) != (tt.wantSent == loginRequest{}) || (len(sent) == 1 && sent[0] != tt.wantSent) {
				t.Errorf("server received %+v, want %+v", sent, tt.wantSent)
			}
			if tt.wantErr != "" {
				errContains(t, err, tt.wantErr)
				return
			}
			if err != nil {
				t.Fatalf("Login: %v", err)
			}
			if got != tt.want {
				t.Errorf("Login = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestClientCallLoginCooldown covers what the table cannot: a refused sign-in
// is not retried within the hour, and is retried after it.
func TestClientCallLoginCooldown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		doer := NewMockDoer(gomock.NewController(t))
		refuse := func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK, Status: "200 OK",
				Header: http.Header{"Content-Type": {"application/json"}},
				Body:   io.NopCloser(strings.NewReader(`{"code":1001,"message":"Sign-in failed."}`)),
			}, nil
		}
		// One sign-in now, one after the cool-down; the call in between must
		// not reach the transport.
		doer.EXPECT().Do(gomock.Any()).DoAndReturn(refuse).Times(2)

		c, err := New("", WithLogin(testCreds), WithHTTPClient(doer))
		if err != nil {
			t.Fatal(err)
		}
		var refused *LoginError
		if _, err := c.Call(t.Context(), "device_status_inquiry", nil); !errors.As(err, &refused) {
			t.Fatalf("first call: %v, want a LoginError", err)
		}
		_, err = c.Call(t.Context(), "device_status_inquiry", nil)
		if !errors.As(err, &refused) || !strings.Contains(err.Error(), "not retried for another 1h0m0s") {
			t.Fatalf("second call: %v, want the remembered refusal", err)
		}
		time.Sleep(loginCooldown)
		if _, err := c.Call(t.Context(), "device_status_inquiry", nil); !errors.As(err, &refused) {
			t.Fatalf("third call: %v, want a fresh LoginError", err)
		}
	})
}
