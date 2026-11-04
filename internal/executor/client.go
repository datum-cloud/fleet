package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"go.datum.net/fleet/internal/simulator"
)

const maxResponseBytes = 64 << 10

var (
	ErrConflict       = errors.New("executor operation conflicts with retained input")
	ErrFenced         = errors.New("executor operation is fenced")
	ErrInvalid        = errors.New("executor rejected the operation")
	ErrOutcomeUnknown = errors.New("executor operation outcome is unknown")
)

// Client is the exact provider-executor contract used by the standalone
// lifecycle library. Implementations must preserve the operation ID and return
// only a verified terminal result.
type Client interface {
	Execute(context.Context, simulator.Request) (simulator.OperationResult, error)
	Lookup(context.Context, string) (simulator.OperationResult, bool, error)
}

type HTTPClient struct {
	base  *url.URL
	token string
	http  *http.Client
}

func NewHTTPClient(baseURL, bearerToken string, client *http.Client) (*HTTPClient, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("executor base URL must be an absolute HTTP URL without query or fragment")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("executor base URL must use http or https")
	}
	if strings.TrimSpace(bearerToken) == "" || strings.ContainsAny(bearerToken, "\r\n") {
		return nil, errors.New("executor bearer token must be nonempty and single-line")
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	} else if client.Timeout <= 0 {
		return nil, errors.New("executor HTTP client must have a positive timeout")
	}
	return &HTTPClient{base: parsed, token: bearerToken, http: client}, nil
}

func (c *HTTPClient) Execute(ctx context.Context, request simulator.Request) (simulator.OperationResult, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return simulator.OperationResult{}, fmt.Errorf("encode executor request: %w", err)
	}
	response, err := c.do(ctx, http.MethodPost, "v1/operations", bytes.NewReader(body))
	if err != nil {
		return simulator.OperationResult{}, fmt.Errorf("%w: submit request: %v", ErrOutcomeUnknown, err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusOK {
		result, err := decodeResult(response.Body)
		if err != nil {
			return simulator.OperationResult{}, fmt.Errorf("%w: decode submit result: %v", ErrOutcomeUnknown, err)
		}
		if err := validateResult(result, request.ID, request.Kind); err != nil {
			return simulator.OperationResult{}, fmt.Errorf("%w: %v", ErrOutcomeUnknown, err)
		}
		return result, nil
	}
	code, message := decodeError(response.Body)
	switch response.StatusCode {
	case http.StatusBadRequest:
		return simulator.OperationResult{}, fmt.Errorf("%w: %s", ErrInvalid, message)
	case http.StatusConflict:
		if code == "operation_fenced" {
			return simulator.OperationResult{}, fmt.Errorf("%w: %s", ErrFenced, message)
		}
		return simulator.OperationResult{}, fmt.Errorf("%w: %s", ErrConflict, message)
	case http.StatusServiceUnavailable:
		return simulator.OperationResult{}, fmt.Errorf("%w: %s", ErrOutcomeUnknown, message)
	default:
		return simulator.OperationResult{}, fmt.Errorf("%w: executor returned HTTP %d", ErrOutcomeUnknown, response.StatusCode)
	}
}

func (c *HTTPClient) Lookup(ctx context.Context, operationID string) (simulator.OperationResult, bool, error) {
	if operationID == "" || strings.Contains(operationID, "/") {
		return simulator.OperationResult{}, false, errors.New("operation ID is empty or contains a path separator")
	}
	response, err := c.do(ctx, http.MethodGet, path.Join("v1/operations", url.PathEscape(operationID)), nil)
	if err != nil {
		return simulator.OperationResult{}, false, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
		return simulator.OperationResult{}, false, nil
	}
	if response.StatusCode != http.StatusOK {
		_, message := decodeError(response.Body)
		return simulator.OperationResult{}, false, fmt.Errorf("lookup executor operation: HTTP %d: %s", response.StatusCode, message)
	}
	result, err := decodeResult(response.Body)
	if err != nil {
		return simulator.OperationResult{}, false, err
	}
	if err := validateResult(result, operationID, result.Kind); err != nil {
		return simulator.OperationResult{}, false, err
	}
	return result, true, nil
}

func (c *HTTPClient) do(ctx context.Context, method, relative string, body io.Reader) (*http.Response, error) {
	target := *c.base
	target.Path = path.Join(c.base.Path, relative)
	request, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	return c.http.Do(request)
}

func decodeResult(reader io.Reader) (simulator.OperationResult, error) {
	body, err := io.ReadAll(io.LimitReader(reader, maxResponseBytes+1))
	if err != nil {
		return simulator.OperationResult{}, err
	}
	if len(body) > maxResponseBytes {
		return simulator.OperationResult{}, errors.New("executor response exceeds size limit")
	}
	var result simulator.OperationResult
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return simulator.OperationResult{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return simulator.OperationResult{}, errors.New("executor returned multiple JSON values")
	}
	return result, nil
}

func validateResult(result simulator.OperationResult, id, kind string) error {
	if result.ID != id || result.Kind == "" || (kind != "" && result.Kind != kind) || result.Phase != "Succeeded" || result.CompletedAt == nil {
		return errors.New("executor result does not match the requested terminal operation")
	}
	switch result.Kind {
	case "cleanup":
		if result.Result != "clean" {
			return errors.New("cleanup result lacks clean evidence")
		}
	case "install":
		if result.Result != "installed" {
			return errors.New("install result lacks installed evidence")
		}
	}
	return nil
}

func decodeError(reader io.Reader) (string, string) {
	var response struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(io.LimitReader(reader, maxResponseBytes)).Decode(&response); err != nil {
		return "", "executor returned an unreadable error"
	}
	return response.Code, response.Message
}
