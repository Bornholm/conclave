// Package httpx is a small REST transport shared by the forge backends.
package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultMaxResponseBytes bounds a single response body.
const DefaultMaxResponseBytes int64 = 20 << 20

// HTTPError is returned for non-2xx responses. It never contains the token.
type HTTPError struct {
	StatusCode int
	Method     string
	Path       string
	Message    string
	RateLimit  bool
}

func (e *HTTPError) Error() string {
	msg := fmt.Sprintf("%s %s: HTTP %d", e.Method, e.Path, e.StatusCode)
	if e.RateLimit {
		msg += " (rate limited)"
	}
	if e.Message != "" {
		msg += ": " + e.Message
	}
	return msg
}

// IsStatus reports whether err is an HTTPError with the given status.
func IsStatus(err error, code int) bool {
	var he *HTTPError
	return errors.As(err, &he) && he.StatusCode == code
}

// Client performs authenticated JSON requests against one base URL.
type Client struct {
	baseURL          *url.URL
	token            string
	authScheme       string
	headers          map[string]string
	httpClient       *http.Client
	maxResponseBytes int64
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient injects the underlying http.Client.
func WithHTTPClient(hc *http.Client) Option { return func(c *Client) { c.httpClient = hc } }

// WithToken sets the bearer token and its Authorization scheme ("Bearer", "token").
func WithToken(scheme, token string) Option {
	return func(c *Client) { c.authScheme, c.token = scheme, token }
}

// WithHeader adds a header to every request.
func WithHeader(name, value string) Option {
	return func(c *Client) { c.headers[name] = value }
}

// WithMaxResponseBytes bounds response bodies.
func WithMaxResponseBytes(n int64) Option { return func(c *Client) { c.maxResponseBytes = n } }

// New creates a client. baseURL must be absolute; a trailing slash is normalized.
func New(baseURL string, opts ...Option) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("invalid base URL %q", baseURL)
	}
	c := &Client{
		baseURL:          u,
		headers:          map[string]string{},
		httpClient:       &http.Client{Timeout: 60 * time.Second},
		maxResponseBytes: DefaultMaxResponseBytes,
	}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

// BaseURL returns the configured base URL.
func (c *Client) BaseURL() *url.URL { return c.baseURL }

// Resolve builds an absolute URL for a path relative to the base URL.
func (c *Client) Resolve(path string, query url.Values) string {
	u := *c.baseURL
	u.Path = strings.TrimRight(u.Path, "/") + "/" + strings.TrimLeft(path, "/")
	if len(query) > 0 {
		u.RawQuery = query.Encode()
	}
	return u.String()
}

// Response is the outcome of a request.
type Response struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

// Do performs a request. absURL may be a path (resolved against the base URL)
// or an absolute URL, in which case the token is only sent when the host
// matches the base URL.
func (c *Client) Do(ctx context.Context, method, absURL string, body any, accept string) (*Response, error) {
	if !strings.Contains(absURL, "://") {
		absURL = c.Resolve(absURL, nil)
	}
	target, err := url.Parse(absURL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	var rd io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, absURL, rd)
	if err != nil {
		return nil, err
	}
	if accept == "" {
		accept = "application/json"
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "conclave")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	if c.token != "" && sameHost(target, c.baseURL) {
		req.Header.Set("Authorization", c.authScheme+" "+c.token)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, target.Path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, c.maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%s %s: read body: %w", method, target.Path, err)
	}
	if int64(len(data)) > c.maxResponseBytes {
		return nil, fmt.Errorf("%s %s: response exceeds %d bytes", method, target.Path, c.maxResponseBytes)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, newHTTPError(method, target.Path, resp, data)
	}
	return &Response{StatusCode: resp.StatusCode, Header: resp.Header, Body: data}, nil
}

// GetJSON performs a GET and decodes the JSON body into out.
func (c *Client) GetJSON(ctx context.Context, path string, query url.Values, out any) (*Response, error) {
	u := path
	if !strings.Contains(path, "://") {
		u = c.Resolve(path, query)
	}
	resp, err := c.Do(ctx, http.MethodGet, u, nil, "")
	if err != nil {
		return nil, err
	}
	if out != nil {
		if err := json.Unmarshal(resp.Body, out); err != nil {
			return nil, fmt.Errorf("GET %s: decode response: %w", path, err)
		}
	}
	return resp, nil
}

func newHTTPError(method, path string, resp *http.Response, data []byte) *HTTPError {
	he := &HTTPError{StatusCode: resp.StatusCode, Method: method, Path: path}
	var payload struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &payload) == nil && payload.Message != "" {
		he.Message = payload.Message
	} else if len(data) > 0 && len(data) < 200 {
		he.Message = strings.TrimSpace(string(data))
	}
	if resp.StatusCode == http.StatusTooManyRequests ||
		(resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0") {
		he.RateLimit = true
	}
	return he
}

func sameHost(a, b *url.URL) bool {
	return strings.EqualFold(a.Host, b.Host) && a.Scheme == b.Scheme
}

// NextLink parses an RFC 5988 Link header and returns the rel="next" URL.
func NextLink(h http.Header) string {
	for _, part := range strings.Split(h.Get("Link"), ",") {
		segs := strings.Split(strings.TrimSpace(part), ";")
		if len(segs) < 2 {
			continue
		}
		u := strings.Trim(strings.TrimSpace(segs[0]), "<>")
		for _, p := range segs[1:] {
			if strings.TrimSpace(p) == `rel="next"` {
				return u
			}
		}
	}
	return ""
}
