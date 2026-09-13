package aqaramcp

import "net/http"

//go:generate go tool mockgen -source=interfaces.go -destination=mocks_test.go -package=aqaramcp

// Doer performs HTTP requests. *http.Client satisfies it, and so does any
// wrapper adding retries, tracing or rate limiting. It is called while the
// client holds its session lock, so a wrapper must not call back into the
// [Client].
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}
