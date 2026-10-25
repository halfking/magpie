package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A Codex asking for one of its own models by its bare name — the Codex
// app's picker does, by the name the ChatGPT backend lists it under —
// means that model: magpie's own ChatGPT account serves it, not another
// provider's model of the same name.
func TestCodexBareModelReachesTheChatGPTAccount(t *testing.T) {
	codexSignedIn(t)
	// a relay with a model of the same name: the bare name used to reach
	// it, the first provider serving it
	relay := &relayUpstream{}
	srv := httptest.NewServer(relay)
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Models: []string{"gpt-5.5"}, Responses: srv.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	// the ChatGPT account's own model of that name
	if err := provider.Save(provider.Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	// the ChatGPT backend the account's own requests go to
	backend := &relayUpstream{}
	up := httptest.NewServer(backend)
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"gpt-5.5","input":"hi"}`))
	req.Header.Set("User-Agent", "codex_cli_rs/0.162.0-alpha.17.2 (Mac OS 26.2.0; arm64)")
	req.Header.Set("originator", "codex_cli_rs")
	req.Header.Set("Authorization", "Bearer magpie")
	req.Header.Set("x-openai-actor-authorization", "magpie")
	New().Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if len(backend.asked()) == 0 {
		t.Fatalf("the ChatGPT account was not asked for the bare name; the relay was asked %d times", len(relay.asked()))
	}
	if len(relay.asked()) != 0 {
		t.Errorf("the relay was asked for the bare name: %d times", len(relay.asked()))
	}
}
