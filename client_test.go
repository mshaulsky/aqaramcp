package aqaramcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"go.uber.org/mock/gomock"
)

// call is one tools/call request the fake server received.
type call struct {
	tool string
	args string // the arguments field, as it arrived
}

// server is a stand-in for the Aqara MCP server: it checks every request the
// way the real one would, runs the session handshake, records tool calls and
// answers them from the case under test.
type server struct {
	mu      sync.Mutex
	methods []string // every JSON-RPC method seen, in order
	calls   []call
	session string // the session ID currently honoured; empty means none
	issued  int    // sessions handed out so far

	sse          bool                                           // answer with event streams instead of plain JSON
	split        bool                                           // spread each stream payload over two data lines
	unauthorized bool                                           // reject everything as the real server does a bad key
	protocol     string                                         // revision to settle on; empty means the client's
	failAck      bool                                           // refuse the next initialised notification
	misnumber    bool                                           // answer with a response ID that is not the request's
	rpcError     *RPCError                                      // fail tools/call at the JSON-RPC layer
	reply        func(tool string, args json.RawMessage) string // JSON of the ToolResult to answer with

	initWithSession bool // an initialise request arrived carrying a session ID

	key         string         // the bearer the server honours; empty means testKey
	logins      []loginRequest // sign-ins received, in order
	issuedKeys  int            // keys handed out by sign-ins so far
	loginCode   int            // refuse sign-ins with this code; 0 issues a key
	loginBroken bool           // answer sign-ins with the 400 the real one gives an unreadable request
}

const testKey = "test-api-key"

// testDigest is the MD5 of "correct horse"; testCreds sign in with it.
const testDigest = "3cb4e732631f47e6eb961f34554b7cde"

var testCreds = Credentials{Username: "home@example.com", PasswordMD5: testDigest, Region: RegionRU}

// errTransport is returned by a deliberately broken HTTP client.
var errTransport = errors.New("transport failure")

func TestNew(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		opts    []Option
		want    string // expected endpoint
		wantErr string
	}{
		{
			name: "defaults to the hosted server",
			key:  testKey,
			want: "https://agent.aqara.com/open/mcp",
		},
		{
			name: "an endpoint override loses its trailing slashes",
			key:  testKey,
			opts: []Option{WithEndpoint("https://agent.aqara.com/open/mcp//")},
			want: "https://agent.aqara.com/open/mcp",
		},
		{
			name: "empty options are ignored",
			key:  testKey,
			opts: []Option{WithEndpoint(""), WithHTTPClient(nil)},
			want: "https://agent.aqara.com/open/mcp",
		},
		{
			name: "credentials stand in for the key",
			opts: []Option{WithLogin(testCreds)},
			want: "https://agent.aqara.com/open/mcp",
		},
		{
			name:    "credentials are checked up front",
			opts:    []Option{WithLogin(Credentials{Username: "home@example.com", Region: RegionRU})},
			wantErr: "aqaramcp: login: password MD5 is required",
		},
		{
			name:    "the key or credentials are required",
			wantErr: "aqaramcp: API key or login credentials are required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := New(tt.key, tt.opts...)
			if tt.wantErr != "" {
				errContains(t, err, tt.wantErr)
				return
			}
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if c.endpoint != tt.want {
				t.Errorf("endpoint = %q, want %q", c.endpoint, tt.want)
			}
		})
	}
}

func TestClientTools(t *testing.T) {
	tests := []struct {
		name        string
		server      *server
		want        []string // tool names
		wantMethods []string
		wantErr     string
	}{
		{
			name:        "opens the session and lists the tools",
			server:      &server{},
			want:        []string{"device_base_inquiry", "device_status_inquiry"},
			wantMethods: []string{"initialize", "notifications/initialized", "tools/list"},
		},
		{
			name:        "reads an event stream just the same",
			server:      &server{sse: true},
			want:        []string{"device_base_inquiry", "device_status_inquiry"},
			wantMethods: []string{"initialize", "notifications/initialized", "tools/list"},
		},
		{
			// The fake refuses every later request that does not echo the
			// revision it settled on, so a pass proves the echo.
			name:        "echoes the protocol revision the server settles on",
			server:      &server{protocol: "2025-03-26"},
			want:        []string{"device_base_inquiry", "device_status_inquiry"},
			wantMethods: []string{"initialize", "notifications/initialized", "tools/list"},
		},
		{
			name:    "a rejected key is reported as it arrived",
			server:  &server{unauthorized: true},
			wantErr: `aqaramcp: list tools: open session: mcp http initialize: 401 Unauthorized: {"error":"unauthorized or expired"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, s := newServer(t, tt.server)

			tools, err := c.Tools(context.Background())
			if tt.wantErr != "" {
				errContains(t, err, tt.wantErr)
				return
			}
			if err != nil {
				t.Fatalf("Tools: %v", err)
			}
			names := make([]string, len(tools))
			for i, tool := range tools {
				names[i] = tool.Name
			}
			if strings.Join(names, ",") != strings.Join(tt.want, ",") {
				t.Errorf("tools = %v, want %v", names, tt.want)
			}
			if got := s.seen(); strings.Join(got, ",") != strings.Join(tt.wantMethods, ",") {
				t.Errorf("methods = %v, want %v", got, tt.wantMethods)
			}
		})
	}
}

func TestClientCall(t *testing.T) {
	answer := `{"content":[{"type":"text","text":"{\"message\":\"ok\"}"}],"structuredContent":{"result":{"message":"ok"}}}`

	tests := []struct {
		name        string
		server      *server
		broken      bool // use an HTTP client that always fails
		login       bool // build the client with credentials instead of the key
		tool        string
		args        any
		before      func(t *testing.T, c *Client, s *server) // runs on an open session, before the call
		want        ToolResult
		wantArgs    string
		wantMethods []string
		wantLogins  int // sign-ins the server received over the whole case
		wantErr     string
	}{
		{
			name:        "signs in first when built with credentials",
			server:      &server{reply: constant(answer)},
			login:       true,
			tool:        "device_status_inquiry",
			want:        ToolResult{Content: []Content{{Type: "text", Text: `{"message":"ok"}`}}, StructuredContent: json.RawMessage(`{"result":{"message":"ok"}}`)},
			wantMethods: []string{"initialize", "notifications/initialized", "tools/call"},
			wantLogins:  1,
		},
		{
			name:        "replaces an expired key and makes the call again",
			server:      &server{reply: constant(answer)},
			login:       true,
			tool:        "device_status_inquiry",
			before:      func(_ *testing.T, _ *Client, s *server) { s.expire() },
			want:        ToolResult{Content: []Content{{Type: "text", Text: `{"message":"ok"}`}}, StructuredContent: json.RawMessage(`{"result":{"message":"ok"}}`)},
			wantMethods: []string{"initialize", "notifications/initialized", "tools/call"},
			wantLogins:  2, // once to open, once after the expiry
		},
		{
			name:    "a static key is not replaced",
			server:  &server{},
			tool:    "device_status_inquiry",
			before:  func(_ *testing.T, _ *Client, s *server) { s.expire() },
			wantErr: `aqaramcp: call device_status_inquiry: mcp http tools/call: 401 Unauthorized: {"error":"unauthorized or expired"}`,
		},
		{
			name:       "a refused sign-in is reported in the service's words",
			server:     &server{loginCode: 1001},
			login:      true,
			tool:       "device_status_inquiry",
			wantLogins: 1,
			wantErr:    "aqaramcp: call device_status_inquiry: login refused: Sign-in failed. Check your account, password, or region and try again. (code 1001)",
		},
		{
			name:   "runs a tool and returns its answer",
			server: &server{reply: constant(answer)},
			tool:   "device_status_inquiry",
			args:   map[string][]string{"device_ids": {"Aqr~1"}},
			want: ToolResult{
				Content:           []Content{{Type: "text", Text: `{"message":"ok"}`}},
				StructuredContent: json.RawMessage(`{"result":{"message":"ok"}}`),
			},
			wantArgs:    `{"device_ids":["Aqr~1"]}`,
			wantMethods: []string{"initialize", "notifications/initialized", "tools/call"},
		},
		{
			name:   "nil arguments send no arguments",
			server: &server{reply: constant(answer)},
			tool:   "device_status_inquiry",
			want: ToolResult{
				Content:           []Content{{Type: "text", Text: `{"message":"ok"}`}},
				StructuredContent: json.RawMessage(`{"result":{"message":"ok"}}`),
			},
			wantArgs:    "",
			wantMethods: []string{"initialize", "notifications/initialized", "tools/call"},
		},
		{
			name:   "picks the answer out of an event stream",
			server: &server{sse: true, reply: constant(answer)},
			tool:   "device_status_inquiry",
			want: ToolResult{
				Content:           []Content{{Type: "text", Text: `{"message":"ok"}`}},
				StructuredContent: json.RawMessage(`{"result":{"message":"ok"}}`),
			},
			wantMethods: []string{"initialize", "notifications/initialized", "tools/call"},
		},
		{
			name:   "a tool's own failure is passed through",
			server: &server{reply: constant(`{"content":[{"type":"text","text":"nope"}],"isError":true}`)},
			tool:   "device_log_inquiry",
			want: ToolResult{
				Content: []Content{{Type: "text", Text: "nope"}},
				IsError: true,
			},
			wantMethods: []string{"initialize", "notifications/initialized", "tools/call"},
		},
		{
			name:   "a forgotten session is reopened and the call repeated",
			server: &server{reply: constant(answer)},
			tool:   "device_status_inquiry",
			before: func(_ *testing.T, _ *Client, s *server) { s.forget() },
			want: ToolResult{
				Content:           []Content{{Type: "text", Text: `{"message":"ok"}`}},
				StructuredContent: json.RawMessage(`{"result":{"message":"ok"}}`),
			},
			// Recorded after the session was opened and the record reset.
			wantMethods: []string{"tools/call", "initialize", "notifications/initialized", "tools/call"},
		},
		{
			name:   "a payload spread over several data lines is reassembled",
			server: &server{sse: true, split: true, reply: constant(answer)},
			tool:   "device_status_inquiry",
			want: ToolResult{
				Content:           []Content{{Type: "text", Text: `{"message":"ok"}`}},
				StructuredContent: json.RawMessage(`{"result":{"message":"ok"}}`),
			},
			wantMethods: []string{"initialize", "notifications/initialized", "tools/call"},
		},
		{
			// The first attempt fails after initialise; the second must not
			// carry the session that attempt was given.
			name:   "a half-opened session is opened afresh",
			server: &server{reply: constant(answer)},
			tool:   "device_status_inquiry",
			before: func(t *testing.T, c *Client, s *server) {
				s.forget()
				s.refuseAck()
				if _, err := c.Call(context.Background(), "device_status_inquiry", nil); err == nil {
					t.Fatal("the first call should fail on the refused acknowledgement")
				}
			},
			want: ToolResult{
				Content:           []Content{{Type: "text", Text: `{"message":"ok"}`}},
				StructuredContent: json.RawMessage(`{"result":{"message":"ok"}}`),
			},
			wantMethods: []string{
				"tools/call", "initialize", "notifications/initialized", // refused, then the acknowledgement fails
				"initialize", "notifications/initialized", "tools/call", // the next call opens afresh
			},
		},
		{
			name:    "an answer to some other request is refused",
			server:  &server{misnumber: true},
			tool:    "device_status_inquiry",
			wantErr: "aqaramcp: call device_status_inquiry: open session: initialize: response carries id 999, want 1",
		},
		{
			name:    "an RPC-level refusal carries its code",
			server:  &server{rpcError: &RPCError{Code: -32602, Message: "Unknown tool: nothing"}},
			tool:    "nothing",
			wantErr: "aqaramcp: call nothing: mcp tools/call: rpc error -32602: Unknown tool: nothing",
		},
		{
			name:    "a transport failure is wrapped",
			server:  &server{},
			broken:  true,
			tool:    "device_status_inquiry",
			wantErr: "aqaramcp: call device_status_inquiry: open session: initialize: " + errTransport.Error(),
		},
		{
			name:    "the tool name is required",
			server:  &server{},
			wantErr: "aqaramcp: tool name is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var opts []Option
			if tt.broken {
				opts = append(opts, WithHTTPClient(brokenDoer(t)))
			}
			key := testKey
			if tt.login {
				key = ""
				opts = append(opts, WithLogin(testCreds))
			}
			c, s := newClient(t, tt.server, key, opts...)
			if tt.before != nil {
				// Open the session first, so the case can tamper with it.
				if _, err := c.Tools(context.Background()); err != nil {
					t.Fatalf("Tools: %v", err)
				}
				s.reset()
				tt.before(t, c, s)
			}

			got, err := c.Call(context.Background(), tt.tool, tt.args)
			if s.staleInit() {
				t.Errorf("an initialise request carried a stale session ID")
			}
			if got := len(s.signIns()); got != tt.wantLogins {
				t.Errorf("sign-ins = %d, want %d", got, tt.wantLogins)
			}
			if tt.wantErr != "" {
				errContains(t, err, tt.wantErr)
				return
			}
			if err != nil {
				t.Fatalf("Call: %v", err)
			}
			if !sameResult(got, tt.want) {
				t.Errorf("result = %+v, want %+v", got, tt.want)
			}
			if got := s.seen(); strings.Join(got, ",") != strings.Join(tt.wantMethods, ",") {
				t.Errorf("methods = %v, want %v", got, tt.wantMethods)
			}
			if last := s.lastCall(); last.tool != tt.tool || last.args != tt.wantArgs {
				t.Errorf("call = %+v, want tool %q args %q", last, tt.tool, tt.wantArgs)
			}
		})
	}
}

func TestToolResultText(t *testing.T) {
	tests := []struct {
		name   string
		result ToolResult
		want   string
	}{
		{
			name:   "joins the text parts",
			result: ToolResult{Content: []Content{{Type: "text", Text: "one"}, {Type: "text", Text: "two"}}},
			want:   "one\ntwo",
		},
		{
			name:   "skips other kinds of content",
			result: ToolResult{Content: []Content{{Type: "image"}, {Type: "text", Text: "only"}}},
			want:   "only",
		},
		{
			name: "nothing gives nothing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.result.Text(); got != tt.want {
				t.Errorf("Text = %q, want %q", got, tt.want)
			}
		})
	}
}

// newServer starts a fake server and returns it with a client aimed at it,
// holding the key the server honours.
func newServer(t *testing.T, s *server, opts ...Option) (*Client, *server) {
	t.Helper()
	return newClient(t, s, testKey, opts...)
}

// newClient starts a fake server and returns it with a client built with the
// given key, which may be empty when the options carry credentials.
func newClient(t *testing.T, s *server, key string, opts ...Option) (*Client, *server) {
	t.Helper()
	if s == nil {
		s = &server{}
	}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)

	c, err := New(key, append([]Option{WithEndpoint(srv.URL)}, opts...)...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c, s
}

// ServeHTTP checks the request, runs the protocol and answers.
func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	if r.URL.Path == loginPath {
		s.serveLogin(w, r, body)
		return
	}
	if s.unauthorized || r.Header.Get("Authorization") != "Bearer "+s.bearer() {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"unauthorized or expired"}`)
		return
	}
	if reason := verifyRequest(r, body); reason != "" {
		http.Error(w, reason, http.StatusBadRequest)
		return
	}

	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"params"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "body is not JSON-RPC: "+err.Error(), http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	s.methods = append(s.methods, req.Method)
	if req.Method == "initialize" {
		if r.Header.Get("Mcp-Session-Id") != "" {
			s.initWithSession = true
		}
		s.issued++
		s.session = fmt.Sprintf("session-%d", s.issued)
	} else if r.Header.Get("Mcp-Session-Id") != s.session {
		// The real server answers a session it does not know with 404,
		// which is the client's cue to open a new one.
		s.mu.Unlock()
		w.WriteHeader(http.StatusNotFound)
		return
	}
	session := s.session
	if req.Method == "tools/call" {
		s.calls = append(s.calls, call{tool: req.Params.Name, args: string(req.Params.Arguments)})
	}
	protocol := s.protocol
	if protocol == "" {
		protocol = protocolVersion
	}
	// Once the session is open, every request must echo the revision the
	// server settled on.
	if req.Method != "initialize" && r.Header.Get("MCP-Protocol-Version") != protocol {
		s.mu.Unlock()
		http.Error(w, "wrong MCP-Protocol-Version", http.StatusBadRequest)
		return
	}
	failAck := req.Method == "notifications/initialized" && s.failAck
	if failAck {
		s.failAck = false
	}
	s.mu.Unlock()

	w.Header().Set("Mcp-Session-Id", session)
	switch req.Method {
	case "initialize":
		s.answer(w, req.ID, fmt.Sprintf(`{"protocolVersion":%q,"capabilities":{},"serverInfo":{"name":"fake","version":"0"}}`, protocol), nil)
	case "notifications/initialized":
		if failAck {
			http.Error(w, "not now", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	case "tools/list":
		s.answer(w, req.ID, `{"tools":[{"name":"device_base_inquiry","description":"设备基础信息查询"},{"name":"device_status_inquiry","description":"设备状态查询"}]}`, nil)
	case "tools/call":
		if s.rpcError != nil {
			s.answer(w, req.ID, "", s.rpcError)
			return
		}
		result := `{"content":[{"type":"text","text":"{}"}]}`
		if s.reply != nil {
			result = s.reply(req.Params.Name, req.Params.Arguments)
		}
		s.answer(w, req.ID, result, nil)
	default:
		s.answer(w, req.ID, "", &RPCError{Code: -32601, Message: "Method not found"})
	}
}

// answer writes one JSON-RPC response, as plain JSON or as an event stream
// preceded by an unrelated notification, which the real server interleaves.
func (s *server) answer(w http.ResponseWriter, id json.RawMessage, result string, rpcErr *RPCError) {
	if s.misnumber {
		id = json.RawMessage("999")
	}
	var message string
	if rpcErr != nil {
		errJSON, _ := json.Marshal(rpcErr)
		message = fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"error":%s}`, id, errJSON)
	} else {
		message = fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":%s}`, id, result)
	}
	if !s.sse {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, message)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = io.WriteString(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/message\",\"params\":{\"level\":\"info\",\"data\":\"working\"}}\n\n")
	if s.split {
		// One payload over two data lines, which the client must join.
		cut := strings.Index(message, ",")
		_, _ = io.WriteString(w, "event: message\ndata: "+message[:cut]+"\ndata: "+message[cut:]+"\n\n")
		return
	}
	_, _ = io.WriteString(w, "event: message\ndata: "+message+"\n\n")
}

// serveLogin plays the sign-in endpoint: it records the request and issues a
// key the server honours from then on, or refuses the way the real one does.
func (s *server) serveLogin(w http.ResponseWriter, r *http.Request, body []byte) {
	if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
		http.Error(w, "a sign-in is a JSON POST", http.StatusBadRequest)
		return
	}
	var req loginRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "sign-in body is not JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logins = append(s.logins, req)
	w.Header().Set("Content-Type", "application/json")
	switch {
	case s.loginBroken:
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"detail":"Failed to process login credentials. Check encryption/key."}`)
	case s.loginCode != 0:
		_, _ = fmt.Fprintf(w, `{"code":%d,"message":"Sign-in failed. Check your account, password, or region and try again."}`, s.loginCode)
	default:
		s.issuedKeys++
		s.key = fmt.Sprintf("issued-key-%d", s.issuedKeys)
		_, _ = fmt.Fprintf(w, `{"code":0,"message":"success","result":{"api_key":%q,"region":%q}}`, s.key, req.Region)
	}
}

// bearer returns the key the server honours.
func (s *server) bearer() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.key == "" {
		return testKey
	}
	return s.key
}

// expire invalidates the key the client holds, as the real server does after
// some days; only a sign-in yields one that works again.
func (s *server) expire() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.key = "expired"
}

// signIns returns the sign-in requests received, in order.
func (s *server) signIns() []loginRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]loginRequest(nil), s.logins...)
}

// refuseAck makes the server fail the next initialised notification, leaving
// the client with a session it was given but could not finish opening.
func (s *server) refuseAck() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failAck = true
}

// staleInit reports whether any initialise request carried a session ID.
func (s *server) staleInit() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.initWithSession
}

// forget drops the current session, as the real server does after a while.
func (s *server) forget() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.session = ""
}

// reset clears the recorded methods, keeping the session.
func (s *server) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.methods = nil
}

// seen returns the JSON-RPC methods received, in order.
func (s *server) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.methods...)
}

// lastCall returns the most recent tools/call request.
func (s *server) lastCall() call {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.calls) == 0 {
		return call{}
	}
	return s.calls[len(s.calls)-1]
}

// verifyRequest checks what every request must carry, reporting the first
// violation.
func verifyRequest(r *http.Request, body []byte) string {
	switch {
	case r.Method != http.MethodPost:
		return "not a POST"
	case r.Header.Get("Content-Type") != "application/json":
		return "not JSON"
	case !strings.Contains(r.Header.Get("Accept"), "application/json") ||
		!strings.Contains(r.Header.Get("Accept"), "text/event-stream"):
		return "Accept must allow both answer shapes"
	case r.Header.Get("MCP-Protocol-Version") == "":
		return "missing MCP-Protocol-Version"
	case len(body) == 0:
		return "empty body"
	}
	return ""
}

// constant answers every tool call with the same ToolResult JSON.
func constant(result string) func(string, json.RawMessage) string {
	return func(string, json.RawMessage) string { return result }
}

// sameResult compares two tool results, treating raw JSON by value.
func sameResult(got, want ToolResult) bool {
	if got.IsError != want.IsError || len(got.Content) != len(want.Content) {
		return false
	}
	for i := range got.Content {
		if got.Content[i] != want.Content[i] {
			return false
		}
	}
	return string(got.StructuredContent) == string(want.StructuredContent)
}

// brokenDoer returns an HTTP client whose every request fails.
func brokenDoer(t *testing.T) Doer {
	t.Helper()
	doer := NewMockDoer(gomock.NewController(t))
	doer.EXPECT().Do(gomock.Any()).Return(nil, errTransport).AnyTimes()
	return doer
}

// errContains fails the test unless err carries the expected text.
func errContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error containing %q, got nil", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error = %q, want it to contain %q", err, want)
	}
}
