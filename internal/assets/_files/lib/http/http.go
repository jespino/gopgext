// Package http is an HTTP(S) client for extensions built with gopgext, on
// libcurl (TinyGo has no net/http here). Its API follows net/http's client:
//
//	resp, err := http.Get("https://example.com/api")
//	if err != nil { ... }
//	defer resp.Body.Close()
//	data, err := io.ReadAll(resp.Body)
//
// Response bodies are streamed: read them incrementally (bufio, json.Decoder)
// to process large responses without holding them in memory. Waiting for the
// server notices query cancellation (the request then fails with the cancel
// error, and PostgreSQL raises it). A request can't outlive the transaction:
// it's closed when the transaction ends. Only http:// and https:// URLs are
// allowed, also when following redirects.
//
// Importing this package links the extension with libcurl (it must be
// installed, with its headers, to build).
package http

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"gopgext/internal/core"
	"gopgext/pg"
)

//export gopgext_http_start
func cStart(method, url, headers, body *byte, bodyLen, timeoutMs int64, errbuf *byte, id *int64) uintptr

//export gopgext_http_wait_headers
func cWaitHeaders(id int64) int64

//export gopgext_http_read
func cRead(id int64, data **byte, n *int64) int64

//export gopgext_http_consume
func cConsume(id, n int64)

//export gopgext_http_status
func cStatus(id int64) int64

//export gopgext_http_headers
func cHeaders(id int64) *byte

//export gopgext_http_error
func cError(id int64) *byte

//export gopgext_http_close
func cClose(id int64)

// Header is a set of HTTP headers. Keys are canonical (Content-Type).
type Header map[string][]string

// CanonicalHeaderKey returns the canonical form of a header name:
// "content-type" -> "Content-Type".
func CanonicalHeaderKey(s string) string {
	b := []byte(s)
	upper := true
	for i, c := range b {
		if upper && c >= 'a' && c <= 'z' {
			b[i] = c - 'a' + 'A'
		} else if !upper && c >= 'A' && c <= 'Z' {
			b[i] = c - 'A' + 'a'
		}
		upper = c == '-'
	}
	return string(b)
}

// Get returns the first value of the header, or "".
func (h Header) Get(key string) string {
	if v := h[CanonicalHeaderKey(key)]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// Set replaces the values of the header.
func (h Header) Set(key, value string) { h[CanonicalHeaderKey(key)] = []string{value} }

// Add adds a value to the header.
func (h Header) Add(key, value string) {
	key = CanonicalHeaderKey(key)
	h[key] = append(h[key], value)
}

// Del removes the header.
func (h Header) Del(key string) { delete(h, CanonicalHeaderKey(key)) }

// Request is an HTTP request.
type Request struct {
	Method string // GET if empty
	URL    string
	Header Header
	Body   []byte // nil: no body
}

// NewRequest returns a request.
func NewRequest(method, url string, body []byte) *Request {
	return &Request{Method: method, URL: url, Header: Header{}, Body: body}
}

// Response is an HTTP response. Its Body must be closed (or read to the end).
type Response struct {
	StatusCode int    // e.g. 200
	Status     string // e.g. "200 OK"
	Header     Header
	Body       io.ReadCloser
	Request    *Request
}

// Client sends requests.
type Client struct {
	// Timeout limits the whole request, reading the body included (0: no
	// limit). Connecting is limited to 30 seconds (or Timeout if shorter).
	Timeout time.Duration
	// Header is added to every request.
	Header Header
}

// DefaultClient is used by Get, Post and Do.
var DefaultClient = &Client{}

// Get sends a GET request with DefaultClient.
func Get(url string) (*Response, error) { return DefaultClient.Get(url) }

// Post sends a POST request with DefaultClient.
func Post(url, contentType string, body []byte) (*Response, error) {
	return DefaultClient.Post(url, contentType, body)
}

// Do sends a request with DefaultClient.
func Do(req *Request) (*Response, error) { return DefaultClient.Do(req) }

// Get sends a GET request.
func (c *Client) Get(url string) (*Response, error) { return c.Do(NewRequest("GET", url, nil)) }

// Post sends a POST request.
func (c *Client) Post(url, contentType string, body []byte) (*Response, error) {
	req := NewRequest("POST", url, body)
	req.Header.Set("Content-Type", contentType)
	return c.Do(req)
}

func cstr(s string) *byte {
	b := make([]byte, len(s)+1)
	copy(b, s)
	return &b[0]
}

func gostr(p *byte) string {
	if p == nil {
		return ""
	}
	n := 0
	for *(*byte)(unsafe.Add(unsafe.Pointer(p), n)) != 0 {
		n++
	}
	return string(unsafe.Slice(p, n))
}

// Error is a failed request (connection, TLS, timeout...).
type Error struct {
	Method, URL string
	Err         string
}

func (e *Error) Error() string { return fmt.Sprintf("%s %s: %s", e.Method, e.URL, e.Err) }

// Do sends a request and returns once the response headers are received.
func (c *Client) Do(req *Request) (*Response, error) {
	method := req.Method
	if method == "" {
		method = "GET"
	}
	var lines strings.Builder
	for _, h := range []Header{c.Header, req.Header} {
		for k, vs := range h {
			for _, v := range vs {
				if strings.ContainsAny(k+v, "\r\n") {
					return nil, &Error{method, req.URL, "invalid header " + strconv.Quote(k)}
				}
				lines.WriteString(k + ": " + v + "\n")
			}
		}
	}
	var body *byte
	if req.Body != nil {
		body = cstr(string(req.Body))
	}
	errbuf := make([]byte, 256)
	var id int64
	core.Check(cStart(cstr(method), cstr(req.URL), cstr(lines.String()), body, int64(len(req.Body)),
		c.Timeout.Milliseconds(), &errbuf[0], &id))
	if id == 0 {
		return nil, &Error{method, req.URL, gostr(&errbuf[0])}
	}
	for {
		switch cWaitHeaders(id) {
		case 1:
			resp := &Response{StatusCode: int(cStatus(id)), Header: Header{}, Request: req,
				Body: &body_{id: id, method: method, url: req.URL}}
			resp.parseHeaders(gostr(cHeaders(id)))
			return resp, nil
		case -2:
			if err := pg.CheckInterrupts(); err != nil {
				cClose(id)
				return nil, err
			}
		default:
			err := &Error{method, req.URL, gostr(cError(id))}
			cClose(id)
			return nil, err
		}
	}
}

func (r *Response) parseHeaders(raw string) {
	for i, line := range strings.Split(raw, "\n") {
		line = strings.TrimRight(line, "\r")
		if i == 0 {
			// HTTP/1.1 200 OK
			if _, status, ok := strings.Cut(line, " "); ok {
				r.Status = status
			}
			continue
		}
		if k, v, ok := strings.Cut(line, ":"); ok {
			r.Header.Add(strings.TrimSpace(k), strings.TrimSpace(v))
		}
	}
	if r.Status == "" {
		r.Status = strconv.Itoa(r.StatusCode)
	}
}

// body_ streams a response body.
type body_ struct {
	id          int64
	method, url string
	closed      bool
	eof         bool
}

func (b *body_) Read(p []byte) (int, error) {
	if b.closed {
		return 0, errors.New("http: read on closed response body")
	}
	if b.eof {
		return 0, io.EOF
	}
	for {
		var data *byte
		var n int64
		switch cRead(b.id, &data, &n) {
		case 1:
			k := copy(p, unsafe.Slice(data, n))
			cConsume(b.id, int64(k))
			return k, nil
		case 0:
			b.eof = true
			return 0, io.EOF
		case -2:
			if err := pg.CheckInterrupts(); err != nil {
				return 0, err
			}
		default:
			return 0, &Error{b.method, b.url, gostr(cError(b.id))}
		}
	}
}

func (b *body_) Close() error {
	if !b.closed {
		b.closed = true
		cClose(b.id)
	}
	return nil
}
