package confluence

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
)

var _ datasource.Connector = (*Connector)(nil)

// Connector implements datasource.Connector for Confluence Server/Data Center.
type Connector struct{}

func NewConnector() *Connector { return &Connector{} }

func (c *Connector) Type() string { return types.ConnectorTypeConfluence }

func (c *Connector) Validate(ctx context.Context, config *types.DataSourceConfig) error {
	cfg, err := parseConfluenceConfig(config)
	if err != nil {
		return err
	}
	cli := newClient(cfg)
	if err := cli.Ping(ctx); err != nil {
		return fmt.Errorf("confluence connection failed: %w", err)
	}
	return nil
}

func (c *Connector) ListResources(ctx context.Context, config *types.DataSourceConfig) ([]types.Resource, error) {
	cfg, err := parseConfluenceConfig(config)
	if err != nil {
		return nil, err
	}
	cli := newClient(cfg)

	spaces, err := cli.ListSpaces(ctx)
	if err != nil {
		return nil, fmt.Errorf("list confluence spaces: %w", err)
	}

	resources := make([]types.Resource, 0, len(spaces))
	for _, s := range spaces {
		if strings.TrimSpace(s.Key) == "" {
			continue
		}
		desc := strings.TrimSpace(s.Description.Plain.Value)
		if desc == "" {
			desc = s.Type
		}
		resources = append(resources, types.Resource{
			ExternalID:  s.Key,
			Name:        s.Name,
			Type:        "confluence_space",
			Description: desc,
			URL:         cli.browserURL(s.Links.WebUI),
			Metadata: map[string]interface{}{
				"space_key": s.Key,
				"space_id":  strconv.FormatInt(s.ID, 10),
				"status":    s.Status,
			},
		})
	}
	sort.Slice(resources, func(i, j int) bool { return resources[i].ExternalID < resources[j].ExternalID })
	return resources, nil
}

func (c *Connector) FetchAll(ctx context.Context, config *types.DataSourceConfig, resourceIDs []string) ([]types.FetchedItem, error) {
	items, _, err := c.walk(ctx, config, resourceIDs, nil, false)
	return items, err
}

func (c *Connector) FetchIncremental(
	ctx context.Context,
	config *types.DataSourceConfig,
	cursor *types.SyncCursor,
) ([]types.FetchedItem, *types.SyncCursor, error) {
	resourceIDs := config.ResourceIDs
	if len(resourceIDs) == 0 {
		return nil, nil, fmt.Errorf("no resource IDs (space keys) configured")
	}

	var prev *confluenceCursor
	if cursor != nil && cursor.ConnectorCursor != nil {
		var p confluenceCursor
		b, _ := json.Marshal(cursor.ConnectorCursor)
		_ = json.Unmarshal(b, &p)
		prev = &p
	}

	since := time.Time{}
	if cursor != nil && !cursor.LastSyncTime.IsZero() {
		since = cursor.LastSyncTime
	} else if prev != nil && !prev.LastSyncTime.IsZero() {
		since = prev.LastSyncTime
	}
	if since.IsZero() {
		items, next, err := c.walk(ctx, config, resourceIDs, nil, true)
		if err != nil {
			return nil, nil, err
		}
		cursorMap := make(map[string]interface{})
		b, _ := json.Marshal(next)
		_ = json.Unmarshal(b, &cursorMap)
		return items, &types.SyncCursor{
			LastSyncTime:    next.LastSyncTime,
			ConnectorCursor: cursorMap,
		}, nil
	}

	items, next, err := c.fetchUpdated(ctx, config, resourceIDs, prev, since)
	if err != nil {
		return nil, nil, err
	}

	cursorMap := make(map[string]interface{})
	b, _ := json.Marshal(next)
	_ = json.Unmarshal(b, &cursorMap)

	return items, &types.SyncCursor{
		LastSyncTime:    next.LastSyncTime,
		ConnectorCursor: cursorMap,
	}, nil
}

func (c *Connector) fetchUpdated(
	ctx context.Context,
	config *types.DataSourceConfig,
	resourceIDs []string,
	prev *confluenceCursor,
	since time.Time,
) ([]types.FetchedItem, *confluenceCursor, error) {
	cfg, err := parseConfluenceConfig(config)
	if err != nil {
		return nil, nil, err
	}
	cli := newClient(cfg)
	next := &confluenceCursor{
		LastSyncTime:   time.Now().UTC(),
		SpaceVersions: make(map[string]map[string]string),
	}
	var out []types.FetchedItem

	for _, spaceKey := range resourceIDs {
		spaceKey = strings.TrimSpace(spaceKey)
		if spaceKey == "" {
			continue
		}
		if prev != nil && prev.SpaceVersions != nil {
			if prevVersions, ok := prev.SpaceVersions[spaceKey]; ok {
				next.SpaceVersions[spaceKey] = copyVersionMap(prevVersions)
			}
		}
		if next.SpaceVersions[spaceKey] == nil {
			next.SpaceVersions[spaceKey] = make(map[string]string)
		}

		pages, err := cli.SearchUpdatedPages(ctx, spaceKey, since, cfg.GetMaxPages())
		if err != nil {
			return nil, nil, fmt.Errorf("search updated pages for space %s: %w", spaceKey, err)
		}

		kept := 0
		for _, page := range pages {
			if page.Type != "" && page.Type != "page" {
				continue
			}
			versionKey := pageVersionKey(page)
			if prev != nil && prev.SpaceVersions != nil {
				if prevVersions, ok := prev.SpaceVersions[spaceKey]; ok && prevVersions[page.ID] == versionKey {
					next.SpaceVersions[spaceKey][page.ID] = versionKey
					continue
				}
			}

			detail, err := cli.GetPage(ctx, page.ID)
			if err != nil {
				out = append(out, types.FetchedItem{
					ExternalID:       page.ID,
					Title:            page.Title,
					SourceResourceID: spaceKey,
					Metadata: map[string]string{
						"error":     err.Error(),
						"channel":   types.ChannelConfluence,
						"page_id":   page.ID,
						"space_key": spaceKey,
					},
				})
				continue
			}

			next.SpaceVersions[spaceKey][page.ID] = pageVersionKey(detail)
			out = append(out, buildFetchedItem(cli, cfg, detail, spaceKey))
			kept++
		}

		logger.Infof(ctx, "[Confluence] incremental space %s: changed=%d emitted=%d since=%s",
			spaceKey, len(pages), kept, since.UTC().Format(time.RFC3339))
	}
	return out, next, nil
}

func (c *Connector) walk(
	ctx context.Context,
	config *types.DataSourceConfig,
	resourceIDs []string,
	prev *confluenceCursor,
	incremental bool,
) ([]types.FetchedItem, *confluenceCursor, error) {
	cfg, err := parseConfluenceConfig(config)
	if err != nil {
		return nil, nil, err
	}
	if len(resourceIDs) == 0 {
		resourceIDs = config.ResourceIDs
	}
	if len(resourceIDs) == 0 {
		return nil, nil, fmt.Errorf("no resource IDs (space keys) provided")
	}

	cli := newClient(cfg)
	next := &confluenceCursor{
		LastSyncTime:  time.Now().UTC(),
		SpaceVersions: make(map[string]map[string]string),
	}
	var out []types.FetchedItem

	for _, spaceKey := range resourceIDs {
		spaceKey = strings.TrimSpace(spaceKey)
		if spaceKey == "" {
			continue
		}

		pages, err := cli.ListSpacePages(ctx, spaceKey, cfg.GetMaxPages())
		if err != nil {
			return nil, nil, fmt.Errorf("list pages for space %s: %w", spaceKey, err)
		}

		currentPages := make(map[string]bool)
		next.SpaceVersions[spaceKey] = make(map[string]string)
		for _, page := range pages {
			if page.Type != "" && page.Type != "page" {
				continue
			}
			currentPages[page.ID] = true
			versionKey := pageVersionKey(page)
			next.SpaceVersions[spaceKey][page.ID] = versionKey

			if incremental && prev != nil && prev.SpaceVersions != nil {
				if prevVersions, ok := prev.SpaceVersions[spaceKey]; ok && prevVersions[page.ID] == versionKey {
					continue
				}
			}

			detail, err := cli.GetPage(ctx, page.ID)
			if err != nil {
				out = append(out, types.FetchedItem{
					ExternalID:       page.ID,
					Title:            page.Title,
					SourceResourceID: spaceKey,
					Metadata: map[string]string{
						"error":     err.Error(),
						"channel":   types.ChannelConfluence,
						"page_id":   page.ID,
						"space_key": spaceKey,
					},
				})
				continue
			}

			out = append(out, buildFetchedItem(cli, cfg, detail, spaceKey))
		}

		logger.Infof(ctx, "[Confluence] space %s: listed=%d emitted=%d", spaceKey, len(pages), len(out))

		if incremental && prev != nil && prev.SpaceVersions != nil {
			if prevVersions, ok := prev.SpaceVersions[spaceKey]; ok {
				for prevPageID := range prevVersions {
					if !currentPages[prevPageID] {
						out = append(out, types.FetchedItem{
							ExternalID:       prevPageID,
							IsDeleted:        true,
							SourceResourceID: spaceKey,
							Metadata: map[string]string{
								"channel":   types.ChannelConfluence,
								"page_id":   prevPageID,
								"space_key": spaceKey,
							},
						})
					}
				}
			}
		}
	}

	if !incremental {
		return out, nil, nil
	}
	return out, next, nil
}

func copyVersionMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func buildFetchedItem(cli *client, cfg *Config, page content, fallbackSpaceKey string) types.FetchedItem {
	spaceKey := page.Space.Key
	if spaceKey == "" {
		spaceKey = fallbackSpaceKey
	}
	storageHTML := page.Body.Storage.Value
	body := storageToMarkdown(storageHTML)
	contentType := "text/markdown"
	ext := ".md"
	if cfg.IncludeHTML {
		body = storageHTML
		contentType = "text/html"
		ext = ".html"
	}

	ancestors := make([]string, 0, len(page.Ancestors))
	for _, a := range page.Ancestors {
		if a.Title != "" {
			ancestors = append(ancestors, a.Title)
		}
	}

	return types.FetchedItem{
		ExternalID:       page.ID,
		Title:            page.Title,
		Content:          []byte(body),
		ContentType:      contentType,
		FileName:         sanitizeFileName(page.Title) + ext,
		URL:              cli.browserURL(page.Links.WebUI),
		UpdatedAt:        parseConfluenceTime(page.Version.When),
		SourceResourceID: spaceKey,
		Metadata: map[string]string{
			"channel":       types.ChannelConfluence,
			"page_id":       page.ID,
			"space_key":     spaceKey,
			"version":       strconv.Itoa(page.Version.Number),
			"updated_at":    page.Version.When,
			"ancestor_path": strings.Join(ancestors, "/"),
			"author":        page.Version.By.DisplayName,
		},
	}
}
