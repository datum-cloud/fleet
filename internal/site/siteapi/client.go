package siteapi

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

	"go.datum.net/fleet/internal/custody"
	"go.datum.net/fleet/internal/site"
)

// Client is the coordinator's client for one site's API. It implements
// delivery.Site.
type Client struct {
	Base  *url.URL
	Token func() (string, error)
	HTTP  *http.Client
}

// NewClient parses the site's base URL.
func NewClient(base string, token func() (string, error), httpClient *http.Client) (*Client, error) {
	parsed, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, errors.New("siteapi: the site URL must be http or https with a host")
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{Base: parsed, Token: token, HTTP: httpClient}, nil
}

func (c *Client) do(ctx context.Context, method, path string, body, into any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.Base.String()+path, reader)
	if err != nil {
		return err
	}
	token, err := c.Token()
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.HTTP.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return fmt.Errorf("siteapi: %s %s: %s", method, path, response.Status)
	}
	if into == nil {
		return nil
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 4<<20))
	decoder.DisallowUnknownFields()
	return decoder.Decode(into)
}

func (c *Client) Deliver(ctx context.Context, assignment site.Assignment) error {
	return c.do(ctx, http.MethodPut, "/v1/assignments", assignment, nil)
}

func (c *Client) Fence(ctx context.Context, device string) (custody.SiteFence, bool, error) {
	namespace, name, ok := strings.Cut(device, "/")
	if !ok {
		return custody.SiteFence{}, false, errors.New("siteapi: a Device is namespace/name")
	}
	var report FenceReport
	if err := c.do(ctx, http.MethodGet, "/v1/fences/"+url.PathEscape(namespace)+"/"+url.PathEscape(name), nil, &report); err != nil {
		return custody.SiteFence{}, false, err
	}
	return custody.SiteFence{Seen: report.Seen, Stopped: report.Stopped}, true, nil
}

func (c *Client) Results(ctx context.Context) ([]site.Stored, error) {
	var held []site.Stored
	return held, c.do(ctx, http.MethodGet, "/v1/results", nil, &held)
}

func (c *Client) Forget(ctx context.Context, stored site.Stored) error {
	return c.do(ctx, http.MethodDelete, "/v1/results", stored, nil)
}
