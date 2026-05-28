// Package confluence implements the Atlassian Confluence data source connector.
//
// It targets self-hosted Confluence Server/Data Center deployments first, using
// username/password Basic Auth and the classic /rest/api endpoints.
package confluence

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/types"
)

const (
	defaultBaseURL  = "http://confluence.xxx.com:8090"
	defaultMaxDepth = 5
	defaultMaxPages = 5000
)

// Config holds Confluence-specific configuration.
type Config struct {
	BaseURL  string `json:"base_url"`
	Username string `json:"username"`
	Password string `json:"password"`

	MaxDepth    int  `json:"max_depth,omitempty"`
	MaxPages    int  `json:"max_pages,omitempty"`
	IncludeHTML bool `json:"include_html,omitempty"`
}

func (c *Config) GetBaseURL() string {
	raw := strings.TrimSpace(c.BaseURL)
	if raw == "" {
		raw = defaultBaseURL
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	return strings.TrimRight(raw, "/")
}

func (c *Config) GetMaxPages() int {
	if c.MaxPages <= 0 {
		return defaultMaxPages
	}
	return c.MaxPages
}

func (c *Config) GetMaxDepth() int {
	if c.MaxDepth < 0 {
		return defaultMaxDepth
	}
	if c.MaxDepth == 0 {
		return defaultMaxDepth
	}
	return c.MaxDepth
}

func parseConfluenceConfig(config *types.DataSourceConfig) (*Config, error) {
	if config == nil {
		return nil, fmt.Errorf("%w: config is nil", datasource.ErrInvalidConfig)
	}

	var cfg Config
	if b, err := json.Marshal(config.Credentials); err == nil {
		if err := json.Unmarshal(b, &cfg); err != nil {
			return nil, fmt.Errorf("parse confluence credentials: %w", err)
		}
	} else {
		return nil, fmt.Errorf("marshal credentials: %w", err)
	}

	if len(config.Settings) > 0 {
		if b, err := json.Marshal(config.Settings); err == nil {
			var settings Config
			if err := json.Unmarshal(b, &settings); err != nil {
				return nil, fmt.Errorf("parse confluence settings: %w", err)
			}
			if settings.MaxDepth != 0 {
				cfg.MaxDepth = settings.MaxDepth
			}
			if settings.MaxPages != 0 {
				cfg.MaxPages = settings.MaxPages
			}
			cfg.IncludeHTML = settings.IncludeHTML
		} else {
			return nil, fmt.Errorf("marshal settings: %w", err)
		}
	}

	if strings.TrimSpace(cfg.BaseURL) == "" {
		return nil, fmt.Errorf("%w: base_url is required", datasource.ErrInvalidConfig)
	}
	if strings.TrimSpace(cfg.Username) == "" || strings.TrimSpace(cfg.Password) == "" {
		return nil, fmt.Errorf("%w: username and password are required", datasource.ErrInvalidCredentials)
	}
	return &cfg, nil
}

type apiErrorBody struct {
	Message string `json:"message"`
	Status  int    `json:"statusCode"`
}

type userResponse struct {
	Type        string `json:"type"`
	UserName    string `json:"username"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
}

type spaceListResponse struct {
	Results []space `json:"results"`
	Start   int     `json:"start"`
	Limit   int     `json:"limit"`
	Size    int     `json:"size"`
	Links   links   `json:"_links"`
}

type space struct {
	ID          int64            `json:"id"`
	Key         string           `json:"key"`
	Name        string           `json:"name"`
	Type        string           `json:"type"`
	Status      string           `json:"status"`
	Description spaceDescription `json:"description"`
	Links       links            `json:"_links"`
}

type spaceDescription struct {
	Plain struct {
		Value string `json:"value"`
	} `json:"plain"`
}

type contentListResponse struct {
	Results []content `json:"results"`
	Start   int       `json:"start"`
	Limit   int       `json:"limit"`
	Size    int       `json:"size"`
	Links   links     `json:"_links"`
}

type content struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	Status    string    `json:"status"`
	Title     string    `json:"title"`
	Space     space     `json:"space"`
	Version   version   `json:"version"`
	Ancestors []content `json:"ancestors"`
	Body      struct {
		Storage struct {
			Value          string `json:"value"`
			Representation string `json:"representation"`
		} `json:"storage"`
	} `json:"body"`
	Children struct {
		Page struct {
			Results []content `json:"results"`
		} `json:"page"`
		Attachment struct {
			Results []attachment `json:"results"`
		} `json:"attachment"`
	} `json:"children"`
	Links links `json:"_links"`
}

type version struct {
	Number int    `json:"number"`
	When   string `json:"when"`
	By     struct {
		UserName    string `json:"username"`
		DisplayName string `json:"displayName"`
	} `json:"by"`
}

type attachmentListResponse struct {
	Results []attachment `json:"results"`
	Start   int          `json:"start"`
	Limit   int          `json:"limit"`
	Size    int          `json:"size"`
	Links   links        `json:"_links"`
}

type attachment struct {
	ID      string  `json:"id"`
	Title   string  `json:"title"`
	Version version `json:"version"`
	Links   links   `json:"_links"`
}

type links struct {
	Self     string `json:"self"`
	WebUI    string `json:"webui"`
	Download string `json:"download"`
	Next     string `json:"next"`
}

type confluenceCursor struct {
	LastSyncTime  time.Time                    `json:"last_sync_time"`
	SpaceVersions map[string]map[string]string `json:"space_versions,omitempty"`
}

func parseConfluenceTime(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.000-0700",
		"2006-01-02T15:04:05.000Z",
		"2006-01-02T15:04:05-0700",
		"2006-01-02T15:04:05Z",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return t
		}
	}
	return time.Time{}
}

func pageVersionKey(p content) string {
	if p.Version.When != "" {
		return p.Version.When + "#" + strconv.Itoa(p.Version.Number)
	}
	return strconv.Itoa(p.Version.Number)
}

func sanitizeFileName(name string) string {
	if name == "" {
		return "untitled"
	}
	replacer := strings.NewReplacer(
		"/", "_", "\\", "_", ":", "_", "*", "_",
		"?", "_", "\"", "_", "<", "_", ">", "_", "|", "_",
	)
	result := strings.TrimSpace(replacer.Replace(name))
	if result == "" {
		return "untitled"
	}
	const maxBytes = 200
	if len(result) > maxBytes {
		result = result[:maxBytes]
		for len(result) > 0 {
			r, size := utf8.DecodeLastRuneInString(result)
			if r != utf8.RuneError || size != 1 {
				break
			}
			result = result[:len(result)-1]
		}
	}
	return result
}

func redactSecret(s string) string {
	if len(s) < 8 {
		return "***"
	}
	return s[:3] + "..." + s[len(s)-2:]
}
