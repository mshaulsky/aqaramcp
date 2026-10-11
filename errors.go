package aqaramcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// errSessionExpired marks a request the server refused because it no longer
// knows the session, which the client answers by opening a new one.
var errSessionExpired = errors.New("session expired")

// RPCError is an error the server reported at the JSON-RPC layer: a malformed
// request, an unknown method, a tool that does not exist.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"` // whatever detail the server attached
	Method  string          `json:"-"`              // request that produced it
}

// ToolError is a failure a tool reported after running: the platform refused
// the query, or answered with something other than a table. The message is
// the platform's own, such as "Device control action not supported"; behind
// a generic one ("Business error occurred.") the platform often puts a code
// and an explanation, which Code and Detail carry.
type ToolError struct {
	Tool    string
	Message string
	Code    string // the platform's error code, e.g. ERROR.NoHomesUnderAccount; empty when it gave none
	Detail  string // the explanation behind the message, e.g. "No homes under account"
	TraceID string // the platform's own identifier for the call, for support
}

// HTTPError reports a response the client could not use as an MCP answer at
// all — a rejected key, a proxy, an outage.
type HTTPError struct {
	StatusCode int
	Status     string
	Method     string
	Body       string // truncated response body, for diagnosis
}

// LoginError is the service refusing a sign-in — the account, the password
// or the region is wrong — in its own words. Retrying changes nothing.
type LoginError struct {
	Code    int
	Message string
}

// Error implements the error interface.
func (e *RPCError) Error() string {
	return fmt.Sprintf("mcp %s: rpc error %d: %s", e.Method, e.Code, e.Message)
}

// Error implements the error interface: the message, then the detail and
// the code when the platform gave them.
func (e *ToolError) Error() string {
	msg := e.Message
	if e.Detail != "" {
		msg = strings.TrimSuffix(msg, ".") + ": " + e.Detail
	}
	if e.Code != "" {
		msg += " (" + e.Code + ")"
	}
	return fmt.Sprintf("aqara tool %s: %s", e.Tool, msg)
}

// Error implements the error interface.
func (e *HTTPError) Error() string {
	return fmt.Sprintf("mcp http %s: %s: %s", e.Method, e.Status, e.Body)
}

// Error implements the error interface.
func (e *LoginError) Error() string {
	return fmt.Sprintf("login refused: %s (code %d)", e.Message, e.Code)
}
