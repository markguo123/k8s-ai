package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/session"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

func ownershipResolver(users map[string]string, roles map[string][]string) PrincipalResolver {
	return func(_ context.Context, token string) (*store.User, *store.Role, error) {
		name, ok := users[token]
		if !ok {
			return nil, nil, context.Canceled
		}
		return &store.User{ID: "u-" + name, Username: name}, &store.Role{
			ID: "r-" + name, Name: name, Permissions: roles[name],
		}, nil
	}
}

func authedRequest(t *testing.T, method, url, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestAuthenticatedSessionListIsolation(t *testing.T) {
	chat := newFakeChat()
	chat.sessions["alice-s1"] = &session.Session{ID: "alice-s1", UserID: "u-alice", Name: "alice"}
	chat.sessions["bob-s1"] = &session.Session{ID: "bob-s1", UserID: "u-bob", Name: "bob"}
	chat.sessions["legacy"] = &session.Session{ID: "legacy", Name: "legacy"}

	srv := httptest.NewServer(New(chat, "m").AuthenticatedHandler(ownershipResolver(
		map[string]string{"alice-token": "alice", "bob-token": "bob", "admin-token": "admin"},
		map[string][]string{"alice": nil, "bob": nil, "admin": {"*"}},
	)))
	defer srv.Close()

	resp := authedRequest(t, http.MethodGet, srv.URL+"/api/v1/sessions", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated list = %d, want 401", resp.StatusCode)
	}
	resp.Body.Close()

	resp = authedRequest(t, http.MethodGet, srv.URL+"/api/v1/sessions", "alice-token")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("alice list = %d", resp.StatusCode)
	}
	var list []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(list) != 1 || list[0]["id"] != "alice-s1" {
		t.Fatalf("alice sessions = %+v", list)
	}

	resp = authedRequest(t, http.MethodDelete, srv.URL+"/api/v1/sessions/bob-s1", "alice-token")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("alice delete bob = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
	if _, ok := chat.sessions["bob-s1"]; !ok {
		t.Fatal("forbidden delete removed bob's session")
	}

	resp = authedRequest(t, http.MethodGet, srv.URL+"/api/v1/sessions", "admin-token")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin list = %d", resp.StatusCode)
	}
	list = nil
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(list) != 3 {
		t.Fatalf("admin should see all sessions, got %+v", list)
	}

	for _, tc := range []struct {
		method string
		path   string
		body   string
		want   int
	}{
		{http.MethodPost, "/api/v1/sessions/bob-s1/rename", `{"name":"admin-renamed"}`, http.StatusOK},
		{http.MethodPost, "/api/v1/sessions/bob-s1/pin", `{"pinned":true}`, http.StatusOK},
		{http.MethodPost, "/api/v1/sessions/bob-s1/send-message", `{"query":"admin"}`, http.StatusAccepted},
		{http.MethodPost, "/api/v1/sessions/bob-s1/approve", `{"choice":"yes"}`, http.StatusConflict},
		{http.MethodPost, "/api/v1/sessions/bob-s1/cancel", `{}`, http.StatusConflict},
	} {
		req, err := http.NewRequest(tc.method, srv.URL+tc.path, strings.NewReader(tc.body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer admin-token")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Fatalf("admin %s %s = %d, want %d", tc.method, tc.path, resp.StatusCode, tc.want)
		}
	}

	resp = authedRequest(t, http.MethodDelete, srv.URL+"/api/v1/sessions/bob-s1", "admin-token")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin delete other's session = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
	if _, ok := chat.sessions["bob-s1"]; ok {
		t.Fatal("admin delete did not remove bob's session")
	}
}

func TestAuthenticatedSessionCreateAssignsOwner(t *testing.T) {
	chat := newFakeChat()
	srv := httptest.NewServer(New(chat, "m").AuthenticatedHandler(ownershipResolver(
		map[string]string{"alice-token": "alice"},
		map[string][]string{"alice": nil},
	)))
	defer srv.Close()

	resp := authedRequest(t, http.MethodPost, srv.URL+"/api/v1/sessions", "alice-token")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create = %d", resp.StatusCode)
	}
	resp.Body.Close()
	chat.mu.Lock()
	defer chat.mu.Unlock()
	if len(chat.sessions) != 1 {
		t.Fatalf("sessions = %d", len(chat.sessions))
	}
	for _, sess := range chat.sessions {
		if sess.UserID != "u-alice" {
			t.Fatalf("owner = %q, want u-alice", sess.UserID)
		}
	}
}
