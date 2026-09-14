package transport

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
	"sync"
	"time"
)

// Options configures a plain (net/http) Client. BaseURL and Header values
// come from the spec; every field except BaseURL has a working default.
type Options struct {
	BaseURL string
	// APIVersion is sent as x-app-apiversion on every request.
	APIVersion string
	// AppAPIClient is sent as x-app-apiclient (spec header, default "default").
	AppAPIClient string
	Accept       string
	ContentType  string
	UserAgent    string
	Timeout      time.Duration
}

const (
	defaultTimeout      = 30 * time.Second
	defaultAPIVersion   = "2.18"
	defaultAppAPIClient = "default"
	defaultAccept       = "text/event-stream, application/json"
	defaultContentType  = "application/json"
)

// NewPlain returns a Client backed by a stock net/http.Client with a cookie
// jar. httptest-based tests use this constructor; production code prefers
// NewUTLS.
func NewPlain(opt Options) (Client, error) {
	return newShared(&http.Client{
		Timeout:   opt.timeout(),
		Transport: http.DefaultTransport,
	}, opt, nil)
}

func (o Options) timeout() time.Duration {
	if o.Timeout == 0 {
		return defaultTimeout
	}
	return o.Timeout
}

// shared implements Client over an *http.Client. TLS fingerprinting only
// changes how the underlying transport dials, so both the plain and utls
// clients are thin wrappers around this engine.
type shared struct {
	http    *http.Client
	baseURL *url.URL
	opt     Options
	mu      sync.Mutex // guards SetCookie/Cookie jar access
}

var _ Client = (*shared)(nil)

func newShared(httpClient *http.Client, opt Options, jar *cookieJar) (Client, error) {
	base, err := url.Parse(strings.TrimRight(opt.BaseURL, "/"))
	if err != nil || base.Host == "" {
		return nil, fmt.Errorf("invalid base URL %q", opt.BaseURL)
	}
	if jar == nil {
		jar = newCookieJar(base.Hostname())
	}
	httpClient.Jar = jar
	if httpClient.Timeout == 0 {
		httpClient.Timeout = defaultTimeout
	}
	if opt.Accept == "" {
		opt.Accept = defaultAccept
	}
	if opt.ContentType == "" {
		opt.ContentType = defaultContentType
	}
	if opt.AppAPIClient == "" {
		opt.AppAPIClient = defaultAppAPIClient
	}
	if opt.APIVersion == "" {
		opt.APIVersion = defaultAPIVersion
	}
	if opt.UserAgent == "" {
		opt.UserAgent = defaultUserAgent
	}
	return &shared{http: httpClient, baseURL: base, opt: opt}, nil
}

func (c *shared) Get(ctx context.Context, path string) error {
	resp, err := c.do(ctx, http.MethodGet, path, c.minimalHeaders(), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return statusError(resp)
}

func (c *shared) GetJSON(ctx context.Context, path string, out any) error {
	resp, err := c.do(ctx, http.MethodGet, path, c.appHeaders(), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := statusError(resp); err != nil {
		return err
	}
	return decodeBody(resp, out)
}

func (c *shared) GetNoRedirect(ctx context.Context, path string) (int, string, error) {
	noRedirect := c.http.CheckRedirect
	c.http.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}
	defer func() { c.http.CheckRedirect = noRedirect }()

	resp, err := c.do(ctx, http.MethodGet, path, c.minimalHeaders(), nil)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	loc := resp.Header.Get("Location")
	if u, err := resp.Location(); err == nil {
		loc = u.String()
	}
	return resp.StatusCode, loc, nil
}

func (c *shared) PostJSON(ctx context.Context, path string, body, out any) error {
	resp, err := c.post(ctx, path, body, c.appHeaders())
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := statusError(resp); err != nil {
		return err
	}
	return decodeBody(resp, out)
}

func (c *shared) PostJSONNoRedirect(ctx context.Context, path string, body, out any) (int, string, error) {
	noRedirect := c.http.CheckRedirect
	c.http.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}
	defer func() { c.http.CheckRedirect = noRedirect }()

	resp, err := c.post(ctx, path, body, c.appHeaders())
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	loc := resp.Header.Get("Location")
	if u, err := resp.Location(); err == nil {
		loc = u.String()
	}
	// 3xx is a valid outcome here (Location carries the result), so only
	// 4xx/5xx are errors — unlike PostJSON.
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return resp.StatusCode, loc, &StatusError{StatusCode: resp.StatusCode, URL: resp.Request.URL.String(), Body: string(b)}
	}
	if out != nil {
		// Tolerant: non-JSON bodies decode as no-op, like _response_json.
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode, loc, nil
}

func (c *shared) PostSSE(ctx context.Context, path string, body any, onLine func([]byte) error) error {
	resp, err := c.post(ctx, path, body, c.appHeaders())
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return &StatusError{StatusCode: resp.StatusCode, URL: resp.Request.URL.String(), Body: string(b)}
	}
	err = scanLines(resp.Body, onLine)
	if errors.Is(err, ErrStopScanning) {
		return nil
	}
	return err
}

func (c *shared) post(ctx context.Context, path string, body any, headers map[string]string) (*http.Response, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode request body: %w", err)
	}
	req, err := c.newRequest(ctx, http.MethodPost, path, headers, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	return c.http.Do(req)
}

func (c *shared) do(ctx context.Context, method, path string, headers map[string]string, r io.Reader) (*http.Response, error) {
	req, err := c.newRequest(ctx, method, path, headers, r)
	if err != nil {
		return nil, err
	}
	return c.http.Do(req)
}

func (c *shared) newRequest(ctx context.Context, method, path string, headers map[string]string, r io.Reader) (*http.Request, error) {
	u, err := c.resolve(path)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, u, r)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return req, nil
}

func (c *shared) resolve(path string) (string, error) {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path, nil
	}
	if !strings.HasPrefix(path, "/") {
		return "", fmt.Errorf("endpoint path %q must be absolute or start with /", path)
	}
	return c.baseURL.String() + path, nil
}

// minimalHeaders mirrors the reduced header set the reference client uses
// for navigation-style GETs (avoids looking like an XHR on init search).
func (c *shared) minimalHeaders() map[string]string {
	return map[string]string{
		"User-Agent":       c.opt.UserAgent,
		"Referer":          c.baseURL.String() + "/",
		"Origin":           c.baseURL.String(),
		"x-app-apiclient":  c.opt.AppAPIClient,
		"x-app-apiversion": c.opt.APIVersion,
	}
}

// appHeaders are the full headers for JSON/SSE API requests, mirroring the
// set curl_cffi sends when impersonating Chrome (fraud detection on the ask
// endpoint checks for the browser security headers).
func (c *shared) appHeaders() map[string]string {
	return map[string]string{
		"User-Agent":         c.opt.UserAgent,
		"Referer":            c.baseURL.String() + "/",
		"Origin":             c.baseURL.String(),
		"Accept":             c.opt.Accept,
		"Accept-Language":    "en-US,en;q=0.9",
		"Content-Type":       c.opt.ContentType,
		"sec-ch-ua":          "Not)A;brand=\"8\"; Chromium=\"138\", \"Not?A_Brand\";v=\"99\", \"Google Chrome\";v=\"138\"",
		"sec-ch-ua-mobile":   "?0",
		"sec-ch-ua-platform": "Windows",
		"sec-fetch-dest":     "empty",
		"sec-fetch-mode":     "cors",
		"sec-fetch-site":     "same-origin",
		"x-app-apiclient":    c.opt.AppAPIClient,
		"x-app-apiversion":   c.opt.APIVersion,
	}
}

func (c *shared) SetCookie(name, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.http.Jar.SetCookies(c.baseURL, []*http.Cookie{{Name: name, Value: value, Path: "/", Secure: true, HttpOnly: true}})
}

func (c *shared) Cookie(name string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.http.Jar.(*cookieJar).get(c.baseURL, name)
}

func statusError(resp *http.Response) error {
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return &StatusError{StatusCode: resp.StatusCode, URL: resp.Request.URL.String(), Body: string(b)}
	}
	return nil
}

func decodeBody(resp *http.Response, out any) error {
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// ErrStopScanning returned from a PostSSE onLine callback stops reading the
// stream and makes PostSSE return nil (clean end-of-stream), so callers can
// terminate on a final frame without draining a kept-open connection.
var ErrStopScanning = errors.New("stop scanning SSE stream")

// maxSSELine bounds one SSE line (frame). Real answer frames are well under
// this; the cap keeps a hostile or broken stream from growing the buffer
// without limit.
const maxSSELine = 16 << 20

// scanLines splits the stream into lines and calls onLine per non-empty line.
func scanLines(r io.Reader, onLine func([]byte) error) error {
	buf := make([]byte, 0, 64<<10)
	chunk := make([]byte, 32<<10)
	for {
		n, err := r.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
			if len(buf) > maxSSELine {
				return fmt.Errorf("SSE line exceeds %d bytes", maxSSELine)
			}
			for {
				idx := bytes.IndexByte(buf, '\n')
				if idx < 0 {
					break
				}
				line := bytes.TrimRight(buf[:idx], "\r")
				buf = buf[idx+1:]
				if len(bytes.TrimSpace(line)) == 0 {
					continue
				}
				if err := onLine(line); err != nil {
					return err
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}
	}
	if len(bytes.TrimSpace(buf)) > 0 {
		return onLine(bytes.TrimRight(buf, "\r"))
	}
	return nil
}

// StatusError is returned for non-2xx responses.
type StatusError struct {
	StatusCode int
	URL        string
	Body       string
}

func (e *StatusError) Error() string {
	if e.Body != "" {
		return fmt.Sprintf("HTTP %d at %s: %s", e.StatusCode, e.URL, e.Body)
	}
	return fmt.Sprintf("HTTP %d at %s", e.StatusCode, e.URL)
}

const defaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/138.0.0.0 Safari/537.36"
