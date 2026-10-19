package cli

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

	"github.com/phinnerhq/sloppa/internal/api"
)

// client is the sloppa server's API, signed in with a machine token.
type client struct {
	url       string
	token     string
	userAgent string
	http      *http.Client
}

func newClient(c credentials, version string) *client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = dialContext
	// Names under localhost are this machine's, which no proxy can reach.
	transport.Proxy = func(req *http.Request) (*url.URL, error) {
		if isLocalhost(req.URL.Hostname()) {
			return nil, nil
		}
		return http.ProxyFromEnvironment(req)
	}
	return &client{
		url:       strings.TrimRight(c.URL, "/"),
		token:     c.Token,
		userAgent: "sloppa/" + version,
		http:      &http.Client{Transport: transport},
	}
}

// unreachableError is a request that never got an answer.
type unreachableError struct {
	url string
	err error
}

func (e *unreachableError) Error() string { return fmt.Sprintf("couldn't reach %s: %v", e.url, e.err) }
func (e *unreachableError) Unwrap() error { return e.err }

// errorBodyMax bounds how much of a failed response is read, looking for its api.Error.
const errorBodyMax = 64 << 10

// send makes a request and returns the response's JSON as it came, decoded into out when given.
// A response other than 2xx is an *api.Error.
func (c *client) send(ctx context.Context, method, path string, query url.Values, body io.Reader, contentType string, out any) (json.RawMessage, error) {
	target := c.url + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, err
	}
	// Only `sloppa setup` goes without, on a machine that has no token yet.
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, &unreachableError{url: c.url, err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, decodeError(resp)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &unreachableError{url: c.url, err: err}
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return nil, fmt.Errorf("the server sent something else than expected: %w", err)
		}
	}
	return raw, nil
}

// call sends in as JSON, if anything.
func (c *client) call(ctx context.Context, method, path string, query url.Values, in, out any) (json.RawMessage, error) {
	if in == nil {
		return c.send(ctx, method, path, query, nil, "", out)
	}
	body, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	return c.send(ctx, method, path, query, bytes.NewReader(body), "application/json", out)
}

// decodeError reads the api.Error a failed response carries; a proxy's page or an empty body gets
// one saying what the status was.
func decodeError(resp *http.Response) error {
	e := &api.Error{}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodyMax))
	if json.Unmarshal(body, e) != nil || e.Code == "" {
		e = &api.Error{Message: "the server answered " + resp.Status}
	}
	e.Status = resp.StatusCode
	return e
}

func (c *client) session(ctx context.Context) (api.User, json.RawMessage, error) {
	var user api.User
	raw, err := c.call(ctx, http.MethodGet, "/api/session", nil, nil, &user)
	return user, raw, err
}
