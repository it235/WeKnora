package confluence

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/logger"
)

const (
	defaultTimeout  = 30 * time.Second
	defaultPageSize = 100
	userAgent       = "WeKnora-Confluence-Connector/1.0"
)

type client struct {
	baseURL    string
	apiPrefix  string
	username   string
	password   string
	httpClient *http.Client

	logAuthOnce sync.Once
}

func newClient(cfg *Config) *client {
	base := cfg.GetBaseURL()
	prefix := "/rest/api"
	if strings.Contains(base, "atlassian.net") {
		prefix = "/wiki/rest/api"
	}
	return &client{
		baseURL:    base,
		apiPrefix:  prefix,
		username:   strings.TrimSpace(cfg.Username),
		password:   cfg.Password,
		httpClient: &http.Client{Timeout: defaultTimeout},
	}
}

func (c *client) doRequest(ctx context.Context, method, endpoint string, params map[string]string, result interface{}) error {
	const maxRetries = 2
	backoff := []time.Duration{500 * time.Millisecond, time.Second}

	c.logAuthOnce.Do(func() {
		logger.Infof(ctx, "[Confluence] client configured user=%s base=%s", c.username, c.baseURL)
	})

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		reqURL, err := c.buildURL(endpoint, params)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, method, reqURL, nil)
		if err != nil {
			return fmt.Errorf("create request: %w", err)
		}
		req.SetBasicAuth(c.username, c.password)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", userAgent)

		logPath := endpoint
		if len(params) > 0 {
			logPath = logPath + "?" + encodeParams(params)
		}
		if attempt == 0 {
			logger.Infof(ctx, "[Confluence] %s %s", method, logPath)
		} else {
			logger.Infof(ctx, "[Confluence] %s %s (retry %d/%d)", method, logPath, attempt, maxRetries)
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("execute request: %w", err)
			if attempt < maxRetries {
				if sErr := sleepCtx(ctx, backoff[attempt]); sErr != nil {
					return sErr
				}
				continue
			}
			return lastErr
		}

		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			lastErr = fmt.Errorf("read response body: %w", readErr)
			if attempt < maxRetries {
				if sErr := sleepCtx(ctx, backoff[attempt]); sErr != nil {
					return sErr
				}
				continue
			}
			return lastErr
		}

		bodyPreview := truncate(string(body), 500)
		logger.Infof(ctx, "[Confluence] %s %s -> status=%d bodyLen=%d",
			method, logPath, resp.StatusCode, len(body))

		if resp.StatusCode == http.StatusTooManyRequests {
			wait := parseRetryAfter(resp.Header.Get("Retry-After"), backoff[min(attempt, len(backoff)-1)])
			lastErr = fmt.Errorf("confluence rate limited: status=429 body=%s", bodyPreview)
			if attempt < maxRetries {
				if sErr := sleepCtx(ctx, wait); sErr != nil {
					return sErr
				}
				continue
			}
			return lastErr
		}

		if resp.StatusCode >= 500 && resp.StatusCode < 600 {
			lastErr = fmt.Errorf("confluence server error: status=%d body=%s", resp.StatusCode, bodyPreview)
			if attempt < maxRetries {
				if sErr := sleepCtx(ctx, backoff[attempt]); sErr != nil {
					return sErr
				}
				continue
			}
			return lastErr
		}

		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return fmt.Errorf("%w: status=%d body=%s", datasource.ErrInvalidCredentials, resp.StatusCode, bodyPreview)
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			var apiErr apiErrorBody
			_ = json.Unmarshal(body, &apiErr)
			if apiErr.Message != "" {
				return fmt.Errorf("confluence api error: status=%d msg=%s", resp.StatusCode, apiErr.Message)
			}
			return fmt.Errorf("confluence api error: status=%d body=%s", resp.StatusCode, bodyPreview)
		}

		if result != nil {
			if err := json.Unmarshal(body, result); err != nil {
				if looksLikeHTML(body) {
					return fmt.Errorf("decode confluence response: got HTML login page or proxy response instead of JSON")
				}
				return fmt.Errorf("decode confluence response: %w", err)
			}
		}
		return nil
	}
	return lastErr
}

func (c *client) buildURL(endpoint string, params map[string]string) (string, error) {
	if endpoint == "" || endpoint[0] != '/' {
		return "", fmt.Errorf("invalid confluence endpoint %q", endpoint)
	}
	u, err := url.Parse(c.baseURL + c.apiPrefix + endpoint)
	if err != nil {
		return "", fmt.Errorf("parse confluence url: %w", err)
	}
	values := u.Query()
	for k, v := range params {
		if v != "" {
			values.Set(k, v)
		}
	}
	u.RawQuery = values.Encode()
	return u.String(), nil
}

func (c *client) Ping(ctx context.Context) error {
	var resp userResponse
	return c.doRequest(ctx, http.MethodGet, "/user/current", nil, &resp)
}

func (c *client) ListSpaces(ctx context.Context) ([]space, error) {
	var out []space
	start := 0
	for {
		var resp spaceListResponse
		if err := c.doRequest(ctx, http.MethodGet, "/space", map[string]string{
			"start":  strconv.Itoa(start),
			"limit":  strconv.Itoa(defaultPageSize),
			"expand": "description.plain",
		}, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Results...)
		if len(resp.Results) < defaultPageSize {
			break
		}
		start += len(resp.Results)
	}
	return out, nil
}

func (c *client) ListSpacePages(ctx context.Context, spaceKey string, maxPages int) ([]content, error) {
	var out []content
	start := 0
	for {
		remaining := maxPages - len(out)
		if maxPages > 0 && remaining <= 0 {
			break
		}
		limit := defaultPageSize
		if maxPages > 0 && remaining < limit {
			limit = remaining
		}
		var resp contentListResponse
		if err := c.doRequest(ctx, http.MethodGet, "/content", map[string]string{
			"type":     "page",
			"spaceKey": spaceKey,
			"start":    strconv.Itoa(start),
			"limit":    strconv.Itoa(limit),
			"expand":   "version,ancestors,space",
		}, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Results...)
		if len(resp.Results) < limit {
			break
		}
		start += len(resp.Results)
	}
	return out, nil
}

func (c *client) SearchUpdatedPages(ctx context.Context, spaceKey string, since time.Time, maxPages int) ([]content, error) {
	var out []content
	start := 0
	cql := fmt.Sprintf(
		`type = page AND space = "%s" AND lastmodified >= "%s" ORDER BY lastmodified ASC`,
		escapeCQLString(spaceKey),
		since.UTC().Format("2006-01-02 15:04"),
	)

	for {
		remaining := maxPages - len(out)
		if maxPages > 0 && remaining <= 0 {
			break
		}
		limit := defaultPageSize
		if maxPages > 0 && remaining < limit {
			limit = remaining
		}
		var resp contentListResponse
		if err := c.doRequest(ctx, http.MethodGet, "/content/search", map[string]string{
			"cql":    cql,
			"start":  strconv.Itoa(start),
			"limit":  strconv.Itoa(limit),
			"expand": "version,ancestors,space",
		}, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Results...)
		if len(resp.Results) < limit {
			break
		}
		start += len(resp.Results)
	}
	return out, nil
}

func (c *client) GetPage(ctx context.Context, pageID string) (content, error) {
	var resp content
	if err := c.doRequest(ctx, http.MethodGet, "/content/"+url.PathEscape(pageID), map[string]string{
		"expand": "body.storage,version,space,ancestors,children.page,children.attachment",
	}, &resp); err != nil {
		return content{}, err
	}
	return resp, nil
}

func (c *client) GetChildPages(ctx context.Context, pageID string) ([]content, error) {
	var resp contentListResponse
	if err := c.doRequest(ctx, http.MethodGet, "/content/"+url.PathEscape(pageID)+"/child/page", map[string]string{
		"limit":  "100",
		"expand": "version,children.page",
	}, &resp); err != nil {
		return nil, err
	}
	return resp.Results, nil
}

func (c *client) ListPageAttachments(ctx context.Context, pageID string) ([]attachment, error) {
	var resp attachmentListResponse
	if err := c.doRequest(ctx, http.MethodGet, "/content/"+url.PathEscape(pageID)+"/child/attachment", map[string]string{
		"limit":  "100",
		"expand": "version",
	}, &resp); err != nil {
		return nil, err
	}
	return resp.Results, nil
}

func (c *client) browserURL(path string) string {
	if path == "" {
		return c.baseURL
	}
	u, err := url.Parse(path)
	if err == nil && u.IsAbs() {
		return path
	}
	return strings.TrimRight(c.baseURL, "/") + "/" + strings.TrimLeft(path, "/")
}

func encodeParams(params map[string]string) string {
	values := url.Values{}
	for k, v := range params {
		if v != "" {
			values.Set(k, v)
		}
	}
	return values.Encode()
}

func escapeCQLString(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s)
}

func parseRetryAfter(header string, fallback time.Duration) time.Duration {
	if header == "" {
		return fallback
	}
	secs, err := strconv.Atoi(header)
	if err != nil {
		return fallback
	}
	if secs <= 0 {
		return 100 * time.Millisecond
	}
	return time.Duration(secs) * time.Second
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func looksLikeHTML(body []byte) bool {
	s := strings.TrimSpace(strings.ToLower(string(body)))
	return strings.HasPrefix(s, "<!doctype html") || strings.HasPrefix(s, "<html")
}
