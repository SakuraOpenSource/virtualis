package agentclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestClientDoesNotFollowRedirects(t *testing.T) {
	var reached atomic.Bool
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Store(true)
		w.Write([]byte(`{"items":[]}`))
	}))
	defer destination.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	client, err := New(origin.URL, "test-agent-credential")
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Drivers(context.Background())
	if reached.Load() {
		t.Fatal("被控重定向越过信任边界，向另一地址发送了凭据")
	}
	if err == nil {
		t.Fatal("被控重定向应返回错误")
	}
}

func TestClientRejectsAmbiguousEndpoint(t *testing.T) {
	for _, endpoint := range []string{
		"http://user:password@agent.example", "http://agent.example?target=other",
		"http://agent.example#fragment", "http://agent.example:0", "http://agent.example:65536",
	} {
		t.Run(endpoint, func(t *testing.T) {
			if _, err := New(endpoint, "test-agent-credential"); err == nil {
				t.Fatal("有歧义的被控地址未被拒绝")
			}
		})
	}
}
