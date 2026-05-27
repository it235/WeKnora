package confluence

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/datasource"
)

type fakeConfluence struct {
	server *httptest.Server
	mux    *http.ServeMux
	calls  []string
}

func newFakeConfluence() *fakeConfluence {
	f := &fakeConfluence{mux: http.NewServeMux()}
	f.server = httptest.NewServer(f.mux)
	f.handleJSON("/rest/api/user/current", http.StatusOK, userResponse{
		UserName:    "alice",
		DisplayName: "Alice",
	})
	return f
}

func (f *fakeConfluence) Close() { f.server.Close() }

func (f *fakeConfluence) cfg() *Config {
	return &Config{
		BaseURL:  f.server.URL,
		Username: "alice",
		Password: "secret",
	}
}

func (f *fakeConfluence) handleJSON(path string, status int, body interface{}) {
	f.mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		f.calls = append(f.calls, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	})
}

func TestClient_Ping_Success(t *testing.T) {
	f := newFakeConfluence()
	defer f.Close()

	if err := newClient(f.cfg()).Ping(context.Background()); err != nil {
		t.Fatalf("Ping error: %v", err)
	}
}

func TestClient_Ping_SendsBasicAuth(t *testing.T) {
	f := newFakeConfluence()
	defer f.Close()

	f.mux = http.NewServeMux()
	f.server.Config.Handler = f.mux
	var gotUser, gotPassword string
	f.mux.HandleFunc("/rest/api/user/current", func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPassword, _ = r.BasicAuth()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(userResponse{UserName: "alice"})
	})

	if err := newClient(f.cfg()).Ping(context.Background()); err != nil {
		t.Fatalf("Ping error: %v", err)
	}
	if gotUser != "alice" || gotPassword != "secret" {
		t.Fatalf("BasicAuth = %q/%q, want alice/secret", gotUser, gotPassword)
	}
}

func TestClient_Ping_401WrapsInvalidCredentials(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Unauthorized"}`))
	}))
	defer srv.Close()

	err := newClient(&Config{BaseURL: srv.URL, Username: "bad", Password: "bad"}).Ping(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, datasource.ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials, got %v", err)
	}
}

func TestClient_ListSpaces(t *testing.T) {
	f := newFakeConfluence()
	defer f.Close()
	f.handleJSON("/rest/api/space", http.StatusOK, spaceListResponse{
		Results: []space{{Key: "ENG", Name: "Engineering"}, {Key: "OPS", Name: "Operations"}},
	})

	spaces, err := newClient(f.cfg()).ListSpaces(context.Background())
	if err != nil {
		t.Fatalf("ListSpaces: %v", err)
	}
	if len(spaces) != 2 || spaces[0].Key != "ENG" {
		t.Fatalf("spaces = %+v", spaces)
	}
}

func TestClient_SearchUpdatedPages_UsesCQL(t *testing.T) {
	f := newFakeConfluence()
	defer f.Close()

	var gotCQL string
	f.mux.HandleFunc("/rest/api/content/search", func(w http.ResponseWriter, r *http.Request) {
		gotCQL = r.URL.Query().Get("cql")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(contentListResponse{
			Results: []content{{ID: "101", Type: "page", Title: "Changed"}},
		})
	})

	pages, err := newClient(f.cfg()).SearchUpdatedPages(
		context.Background(),
		"ENG",
		parseConfluenceTime("2026-05-20T10:30:00.000Z"),
		100,
	)
	if err != nil {
		t.Fatalf("SearchUpdatedPages: %v", err)
	}
	if len(pages) != 1 || pages[0].ID != "101" {
		t.Fatalf("pages = %+v", pages)
	}
	want := `type = page AND space = "ENG" AND lastmodified >= "2026-05-20 10:30" ORDER BY lastmodified ASC`
	if gotCQL != want {
		t.Fatalf("cql = %q, want %q", gotCQL, want)
	}
}

func TestClient_HTMLLoginPageDecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body>login</body></html>"))
	}))
	defer srv.Close()

	err := newClient(&Config{BaseURL: srv.URL, Username: "alice", Password: "secret"}).Ping(context.Background())
	if err == nil {
		t.Fatal("expected decode error")
	}
}
