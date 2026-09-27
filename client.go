package aqaramcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Client talks to an MCP server over streamable HTTP. It opens the session
// lazily on first use and is safe for concurrent use.
type Client struct {
	endpoint string
	apiKey   string
	creds    *Credentials // sign in for the key when there is none or it is rejected; nil means the key is static
	onLogin  func(Key)    // told about every successful sign-in
	http     Doer

	mu        sync.Mutex // serialises use of the single session and guards its state
	sessionID string
	protocol  string // revision the server settled on when the session opened
	nextID    int64
	ready     bool
	refused   *LoginError // the last refused sign-in, which stands for loginCooldown
	refusedAt time.Time
}

const (
	// DefaultEndpoint is Aqara's hosted MCP server. The login page hands out
	// the same address next to the API key.
	DefaultEndpoint = "https://agent.aqara.com/open/mcp"

	// protocolVersion is the MCP revision this client asks for. The server
	// answers with the one it settles on, which is echoed thereafter.
	protocolVersion = "2025-06-18"

	// clientVersion is what this client calls itself when opening a session.
	clientVersion = "0.2.0"

	// loginCooldown is how long a refused sign-in is left alone: the
	// credentials will not have changed within the hour, and retrying at a
	// polling rate would only invite the service to lock the account.
	loginCooldown = time.Hour

	// defaultTimeout bounds one request, including reading its whole event
	// stream. Tool calls reach into the Aqara cloud behind the scenes, so they
	// are slower than a plain API would be.
	defaultTimeout = 60 * time.Second

	// maxErrorBody caps how much of an unusable response is kept for the
	// error message.
	maxErrorBody = 512

	// maxResponseBody bounds how much of an answer is read — a plain JSON
	// body, or one line of an event stream, which carries a whole device
	// table. Anything approaching it is not the platform.
	maxResponseBody = 4 << 20
)

// Tool describes one operation the server offers.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

// Content is one piece of a tool's answer, usually text.
type Content struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// ToolResult is what a tool call produced, as the MCP layer sees it.
//
// The Aqara server puts its real answer — a JSON envelope with a table in it
// — into the text content. StructuredContent carries the same envelope, but
// not reliably: it is wrapped in a "result" object on success and bare on
// failure, so this package reads the text and callers are advised to do the
// same.
type ToolResult struct {
	Content           []Content       `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
	IsError           bool            `json:"isError,omitempty"`
}

// Option customises a Client.
type Option func(*Client)

// rpcRequest is a JSON-RPC 2.0 request or, when ID is zero, a notification.
type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// rpcResponse is a JSON-RPC 2.0 response.
type rpcResponse struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *RPCError       `json:"error"`
}

// initializeParams opens an MCP session.
type initializeParams struct {
	ProtocolVersion string     `json:"protocolVersion"`
	Capabilities    struct{}   `json:"capabilities"`
	ClientInfo      clientInfo `json:"clientInfo"`
}

// clientInfo names this client to the server.
type clientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// initializeResult is the part of the server's answer the client keeps.
type initializeResult struct {
	ProtocolVersion string `json:"protocolVersion"`
}

// callParams asks the server to run one tool.
type callParams struct {
	Name      string `json:"name"`
	Arguments any    `json:"arguments,omitempty"`
}

// WithEndpoint points the client at a different MCP server, for a test server
// or a deployment this package does not know about.
func WithEndpoint(endpoint string) Option {
	return func(c *Client) {
		if e := strings.TrimRight(endpoint, "/"); e != "" {
			c.endpoint = e
		}
	}
}

// WithHTTPClient supplies the HTTP client used for every call, which is the
// hook for custom timeouts, proxies, retries or instrumentation.
func WithHTTPClient(d Doer) Option {
	return func(c *Client) {
		if d != nil {
			c.http = d
		}
	}
}

// WithLogin makes the client sign in for its key: at first use when New was
// given no key, and again whenever the server rejects the key it holds, in
// which case the call that met the rejection is made once more with the
// fresh key. The service issues keys that live for days, so a long-running
// program keeps working across expiries without anyone visiting the login
// page. A refused sign-in — wrong account, password or region — is reported
// as a [*LoginError] and not retried for an hour.
func WithLogin(creds Credentials) Option {
	return func(c *Client) {
		c.creds = &creds
	}
}

// WithLoginHook registers fn to be called after every successful sign-in
// with the key just issued: to log the event (never the key itself), count
// it, or hand the key to something else. It runs while the client holds its
// lock, so it must not call back into the [Client].
func WithLoginHook(fn func(Key)) Option {
	return func(c *Client) {
		c.onLogin = fn
	}
}

// New creates a client. The key is the one the login page at
// https://agent.aqara.com/login hands out; with [WithLogin] the client signs
// in by itself and the key may be empty. New performs no network I/O; the
// first call signs in if it must and opens the session.
func New(apiKey string, opts ...Option) (*Client, error) {
	c := &Client{
		endpoint: DefaultEndpoint,
		apiKey:   apiKey,
		http:     &http.Client{Timeout: defaultTimeout},
		protocol: protocolVersion,
	}
	for _, o := range opts {
		o(c)
	}
	switch {
	case c.creds != nil:
		if err := c.creds.validate(); err != nil {
			return nil, fmt.Errorf("aqaramcp: %w", err)
		}
	case c.apiKey == "":
		return nil, errors.New("aqaramcp: API key or login credentials are required")
	}
	return c, nil
}

// Tools lists the operations the server offers, with their input schemas.
func (c *Client) Tools(ctx context.Context) ([]Tool, error) {
	var result struct {
		Tools []Tool `json:"tools"`
	}
	if err := c.request(ctx, "tools/list", struct{}{}, &result); err != nil {
		return nil, fmt.Errorf("aqaramcp: list tools: %w", err)
	}
	return result.Tools, nil
}

// Call runs one tool with the given arguments, which must marshal to a JSON
// object or be nil. It is the escape hatch for tools this package does not
// wrap; the typed methods are built on it.
func (c *Client) Call(ctx context.Context, tool string, args any) (ToolResult, error) {
	if tool == "" {
		return ToolResult{}, errors.New("aqaramcp: tool name is required")
	}
	result, err := c.call(ctx, tool, args)
	if err != nil {
		return ToolResult{}, fmt.Errorf("aqaramcp: %w", err)
	}
	return result, nil
}

// Text gathers the textual parts of the answer, which is where this server
// puts everything it has to say.
func (r ToolResult) Text() string {
	var b strings.Builder
	for _, content := range r.Content {
		if content.Type == "text" {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(content.Text)
		}
	}
	return b.String()
}

// request performs one exchange on an open session. With credentials, a
// missing key is obtained first and a rejected one replaced, the call then
// being made once more; New guarantees credentials when the key is empty.
func (c *Client) request(ctx context.Context, method string, params, out any) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.apiKey == "" {
		if err := c.refresh(ctx); err != nil {
			return err
		}
	}
	err := c.exchange(ctx, method, params, out)
	if c.creds != nil && rejected(err) {
		// The key has expired: the server is not forgetting the session,
		// it is refusing the bearer. A fresh key starts a fresh session.
		if err := c.refresh(ctx); err != nil {
			return err
		}
		c.ready, c.sessionID = false, ""
		return c.exchange(ctx, method, params, out)
	}
	return err
}

// exchange opens the session if needed and performs the call, once more on
// a new session if the server has forgotten the old one. The caller must
// hold c.mu.
func (c *Client) exchange(ctx context.Context, method string, params, out any) error {
	if err := c.open(ctx); err != nil {
		return err
	}
	err := c.rpc(ctx, method, params, out)
	if errors.Is(err, errSessionExpired) {
		// Sessions are the server's to end; a forgotten one is not a
		// failure of the call, so the call is made again on a fresh one.
		c.ready, c.sessionID = false, ""
		if err := c.open(ctx); err != nil {
			return err
		}
		return c.rpc(ctx, method, params, out)
	}
	return err
}

// refresh signs in for a new key. A refusal is remembered and stands for
// loginCooldown; a transport failure is not, the next call tries again. The
// caller must hold c.mu.
func (c *Client) refresh(ctx context.Context) error {
	if c.refused != nil {
		if since := time.Since(c.refusedAt); since < loginCooldown {
			return fmt.Errorf("login: refused %s ago, not retried for another %s: %w",
				since.Round(time.Second), (loginCooldown - since).Round(time.Second), c.refused)
		}
	}
	key, err := c.login(ctx, *c.creds)
	var refusal *LoginError
	if errors.As(err, &refusal) {
		c.refused, c.refusedAt = refusal, time.Now()
	}
	if err != nil {
		return err
	}
	c.refused = nil
	c.apiKey = key.APIKey
	if c.onLogin != nil {
		c.onLogin(key)
	}
	return nil
}

// rejected reports whether err is the server refusing the bearer key.
func rejected(err error) bool {
	var httpErr *HTTPError
	return errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusUnauthorized
}

// open starts the MCP session once: initialise, remember the session ID and
// protocol revision the server chose, and acknowledge with the initialised
// notification. The caller must hold c.mu.
func (c *Client) open(ctx context.Context) error {
	if c.ready {
		return nil
	}
	// A session opens without one: an ID left by an attempt that failed
	// after initialise would otherwise travel with the new initialise.
	c.sessionID = ""
	params := initializeParams{
		ProtocolVersion: protocolVersion,
		ClientInfo:      clientInfo{Name: "aqaramcp", Version: clientVersion},
	}
	var result initializeResult
	if err := c.rpc(ctx, "initialize", params, &result); err != nil {
		return fmt.Errorf("open session: %w", err)
	}
	if result.ProtocolVersion != "" {
		c.protocol = result.ProtocolVersion
	}
	if err := c.notify(ctx, "notifications/initialized"); err != nil {
		return fmt.Errorf("open session: %w", err)
	}
	c.ready = true
	return nil
}

// rpc performs one request-response exchange. The caller must hold c.mu.
func (c *Client) rpc(ctx context.Context, method string, params, out any) error {
	c.nextID++
	id := c.nextID
	resp, err := c.post(ctx, rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound && c.sessionID != "" {
		return fmt.Errorf("%s: %w", method, errSessionExpired)
	}
	if resp.StatusCode != http.StatusOK {
		return c.httpError(resp, method)
	}
	if session := resp.Header.Get("Mcp-Session-Id"); session != "" {
		c.sessionID = session
	}

	reply, err := readResponse(resp, id)
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	if reply.Error != nil {
		reply.Error.Method = method
		return reply.Error
	}
	if out == nil || len(reply.Result) == 0 {
		return nil
	}
	if err := json.Unmarshal(reply.Result, out); err != nil {
		return fmt.Errorf("%s: decode result: %w", method, err)
	}
	return nil
}

// notify sends a one-way notification, which the server acknowledges with a
// bare status. The caller must hold c.mu.
func (c *Client) notify(ctx context.Context, method string) error {
	resp, err := c.post(ctx, rpcRequest{JSONRPC: "2.0", Method: method})
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusMultipleChoices {
		return c.httpError(resp, method)
	}
	return nil
}

// post sends one JSON-RPC message with the session's headers.
func (c *Client) post(ctx context.Context, message rpcRequest) (*http.Response, error) {
	body, err := json.Marshal(message)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("MCP-Protocol-Version", c.protocol)
	if c.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", c.sessionID)
	}
	return c.http.Do(req)
}

// httpError describes a response that is not an MCP answer.
func (c *Client) httpError(resp *http.Response, method string) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody+1))
	return &HTTPError{StatusCode: resp.StatusCode, Status: resp.Status, Method: method, Body: truncate(body)}
}

// readResponse extracts the JSON-RPC response with the given ID from either
// answer shape the transport allows: a plain JSON body, or a server-sent
// event stream carrying messages until the response appears.
func readResponse(resp *http.Response, id int64) (rpcResponse, error) {
	want, _ := json.Marshal(id)
	body := io.LimitReader(resp.Body, maxResponseBody)

	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		var reply rpcResponse
		if err := json.NewDecoder(body).Decode(&reply); err != nil {
			return rpcResponse{}, fmt.Errorf("decode response: %w", err)
		}
		if !bytes.Equal(reply.ID, want) {
			return rpcResponse{}, fmt.Errorf("response carries id %s, want %s", reply.ID, want)
		}
		return reply, nil
	}

	// A stream interleaves notifications with the answer; only the message
	// bearing our ID is the answer.
	match := func(data string) (rpcResponse, bool) {
		var reply rpcResponse
		err := json.Unmarshal([]byte(data), &reply)
		return reply, err == nil && bytes.Equal(reply.ID, want)
	}

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), maxResponseBody)
	var data strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "data:"):
			// Several data lines make one payload, joined by newlines.
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		case line == "" && data.Len() > 0:
			if reply, ok := match(data.String()); ok {
				return reply, nil
			}
			data.Reset()
		}
	}
	if err := scanner.Err(); err != nil {
		return rpcResponse{}, fmt.Errorf("read event stream: %w", err)
	}
	// The last event may arrive without its trailing blank line when the
	// server closes the stream right after it.
	if data.Len() > 0 {
		if reply, ok := match(data.String()); ok {
			return reply, nil
		}
	}
	return rpcResponse{}, errors.New("the event stream ended without a response")
}

// truncate keeps an unusable response short enough to belong in an error.
func truncate(b []byte) string {
	if len(b) <= maxErrorBody {
		return string(b)
	}
	cut := maxErrorBody
	for cut > maxErrorBody-utf8.UTFMax && !utf8.RuneStart(b[cut]) {
		cut--
	}
	return string(b[:cut]) + "…"
}
