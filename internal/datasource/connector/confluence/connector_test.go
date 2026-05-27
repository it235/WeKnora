package confluence

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

func makeDSConfig(f *fakeConfluence, resourceIDs []string) *types.DataSourceConfig {
	return &types.DataSourceConfig{
		Type: types.ConnectorTypeConfluence,
		Credentials: map[string]interface{}{
			"base_url": f.cfg().BaseURL,
			"username": f.cfg().Username,
			"password": f.cfg().Password,
		},
		ResourceIDs: resourceIDs,
		Settings: map[string]interface{}{
			"max_pages": 100,
		},
	}
}

func TestConnector_Type(t *testing.T) {
	if NewConnector().Type() != types.ConnectorTypeConfluence {
		t.Fatalf("Type() = %q", NewConnector().Type())
	}
}

func TestConnector_Validate(t *testing.T) {
	f := newFakeConfluence()
	defer f.Close()

	if err := NewConnector().Validate(context.Background(), makeDSConfig(f, nil)); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestConnector_ListResources(t *testing.T) {
	f := newFakeConfluence()
	defer f.Close()
	f.handleJSON("/rest/api/space", 200, spaceListResponse{
		Results: []space{
			{ID: 2, Key: "OPS", Name: "Operations", Type: "global"},
			{ID: 1, Key: "ENG", Name: "Engineering", Type: "global"},
		},
	})

	resources, err := NewConnector().ListResources(context.Background(), makeDSConfig(f, nil))
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	if len(resources) != 2 {
		t.Fatalf("len = %d, want 2", len(resources))
	}
	if resources[0].ExternalID != "ENG" || resources[0].Type != "confluence_space" {
		t.Fatalf("resources sorted/mapped wrong: %+v", resources)
	}
}

func TestConnector_FetchAll_Markdown(t *testing.T) {
	f := newFakeConfluence()
	defer f.Close()
	f.handleJSON("/rest/api/content", 200, contentListResponse{
		Results: []content{
			{
				ID:    "101",
				Type:  "page",
				Title: "Hello",
				Space: space{Key: "ENG"},
				Version: version{
					Number: 3,
					When:   "2026-05-20T10:00:00.000Z",
				},
			},
		},
	})
	page := content{
		ID:    "101",
		Type:  "page",
		Title: "Hello",
		Space: space{Key: "ENG"},
		Version: version{
			Number: 3,
			When:   "2026-05-20T10:00:00.000Z",
		},
		Links: links{WebUI: "/display/ENG/Hello"},
	}
	page.Body.Storage.Value = "<h1>Hello</h1><p><strong>world</strong></p>"
	f.handleJSON("/rest/api/content/101", 200, page)

	items, err := NewConnector().FetchAll(context.Background(), makeDSConfig(f, []string{"ENG"}), []string{"ENG"})
	if err != nil {
		t.Fatalf("FetchAll: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len = %d, want 1", len(items))
	}
	got := items[0]
	if got.ExternalID != "101" || got.Title != "Hello" {
		t.Fatalf("wrong item identity: %+v", got)
	}
	if string(got.Content) != "# Hello\n\n**world**\n" {
		t.Fatalf("markdown = %q", string(got.Content))
	}
	if got.ContentType != "text/markdown" || got.FileName != "Hello.md" {
		t.Fatalf("content fields wrong: %+v", got)
	}
	if got.Metadata["channel"] != types.ChannelConfluence || got.SourceResourceID != "ENG" {
		t.Fatalf("metadata/source wrong: %+v", got)
	}
}

func TestConnector_FetchIncremental_SearchesUpdatedPages(t *testing.T) {
	f := newFakeConfluence()
	defer f.Close()
	f.handleJSON("/rest/api/content/search", 200, contentListResponse{
		Results: []content{
			{
				ID:    "102",
				Type:  "page",
				Title: "Changed",
				Space: space{Key: "ENG"},
				Version: version{
					Number: 4,
					When:   "2026-05-20T11:00:00.000Z",
				},
			},
		},
	})
	page := content{
		ID:    "102",
		Type:  "page",
		Title: "Changed",
		Space: space{Key: "ENG"},
		Version: version{
			Number: 4,
			When:   "2026-05-20T11:00:00.000Z",
		},
	}
	page.Body.Storage.Value = "<p>changed body</p>"
	f.handleJSON("/rest/api/content/102", 200, page)

	cursor := &types.SyncCursor{
		LastSyncTime: parseConfluenceTime("2026-05-20T10:30:00.000Z"),
		ConnectorCursor: map[string]interface{}{
			"space_versions": map[string]interface{}{
				"ENG": map[string]interface{}{
					"101": "2026-05-20T10:00:00.000Z#3",
				},
			},
		},
	}
	items, next, err := NewConnector().FetchIncremental(context.Background(), makeDSConfig(f, []string{"ENG"}), cursor)
	if err != nil {
		t.Fatalf("FetchIncremental: %v", err)
	}
	if next == nil || next.ConnectorCursor == nil {
		t.Fatal("expected next cursor")
	}
	if len(items) != 1 || items[0].ExternalID != "102" || items[0].IsDeleted {
		t.Fatalf("expected one changed page, got %+v", items)
	}
	if string(items[0].Content) != "changed body\n" {
		t.Fatalf("content = %q", string(items[0].Content))
	}
}

func TestConnector_FetchIncremental_SkipsSameVersionFromSearch(t *testing.T) {
	f := newFakeConfluence()
	defer f.Close()
	f.handleJSON("/rest/api/content/search", 200, contentListResponse{
		Results: []content{
			{
				ID:    "101",
				Type:  "page",
				Title: "Same",
				Space: space{Key: "ENG"},
				Version: version{
					Number: 3,
					When:   "2026-05-20T10:00:00.000Z",
				},
			},
		},
	})

	cursor := &types.SyncCursor{
		LastSyncTime: parseConfluenceTime("2026-05-20T09:00:00.000Z"),
		ConnectorCursor: map[string]interface{}{
			"space_versions": map[string]interface{}{
				"ENG": map[string]interface{}{
					"101": "2026-05-20T10:00:00.000Z#3",
					"999": "2026-05-19T10:00:00.000Z#1",
				},
			},
		},
	}
	items, _, err := NewConnector().FetchIncremental(context.Background(), makeDSConfig(f, []string{"ENG"}), cursor)
	if err != nil {
		t.Fatalf("FetchIncremental: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("same version should be skipped and deletes are not inferred in incremental, got %+v", items)
	}
}
