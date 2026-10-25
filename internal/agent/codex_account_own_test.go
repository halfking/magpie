package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A Codex on magpie as its provider is handed a catalog file that stands in
// for Codex's own list: it holds magpie's models only, so the Codex app's
// picker — which asks for Codex's own models by their bare names — has none
// of them, and the app-server reads "Unknown model gpt-a is used".
// The catalog holds Codex's own models by their bare names, each with the
// entry Codex has for it, beside magpie's.
func TestCodexCatalogKeepsOwnModels(t *testing.T) {
	home, read := codexHome(t, `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`, "model = \"gpt-a\"\n")
	dir := filepath.Join(home, ".codex")
	os.WriteFile(filepath.Join(dir, "models_cache.json"), []byte(`{"models":[
		{"slug":"gpt-a","display_name":"A","priority":1,"supported_reasoning_levels":[{"effort":"low"},{"effort":"high"}]},
		{"slug":"gpt-b","display_name":"B","priority":2}]}`), 0o644)
	cx := codex(home)
	// magpie as Codex's provider, so the catalog is written for it
	if err := cx.Field("login").Set("api"); err != nil {
		t.Fatal(err)
	}
	if err := cx.Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	cfg := read()
	if !strings.Contains(cfg, `model_provider = "magpie"`) || !strings.Contains(cfg, "model_catalog_json") {
		t.Fatalf("magpie is not Codex's provider:\n%s", cfg)
	}
	b, err := os.ReadFile(filepath.Join(dir, "magpie-models.json"))
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(b, &list); err != nil {
		t.Fatalf("the catalog is not a models.json: %v\n%s", err, b)
	}
	byslug := map[string]map[string]any{}
	for _, m := range list.Models {
		slug, _ := m["slug"].(string)
		byslug[slug] = m
	}
	// Codex's own model, by the name the ChatGPT backend lists it under
	own, ok := byslug["gpt-a"]
	if !ok {
		t.Fatalf("Codex's own model is not in the catalog magpie wrote:\n%s", b)
	}
	// with the entry Codex has for it, not one of magpie's
	if name, _ := own["display_name"].(string); name != "A" {
		t.Fatalf("Codex's own model is not the entry Codex has: %q", name)
	}
	if desc, _ := own["description"].(string); strings.Contains(desc, "via magpie") {
		t.Fatalf("Codex's own model is written as one of magpie's: %q", desc)
	}
	// magpie's own models are in it too
	if _, ok := byslug["fake/m1"]; !ok {
		t.Fatalf("magpie's own model is not in the catalog:\n%s", b)
	}
}

// The Codex app keeps the ChatGPT account — its plan and its credit balance —
// only when the provider it reads needs OpenAI auth: one that does not leaves
// the app in its API state, with no account to show. The account mode
// says so; the API mode leaves the provider as it was.
func TestCodexAccountModeKeepsTheAccount(t *testing.T) {
	home, read := codexHome(t, `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`, "model = \"gpt-a\"\n")
	dir := filepath.Join(home, ".codex")
	os.WriteFile(filepath.Join(dir, "models_cache.json"), []byte(`{"models":[{"slug":"gpt-a","display_name":"A","priority":1}]}`), 0o644)
	// the account is out of its allowance, so magpie is Codex's provider
	usedUp := codexUsedUp
	codexUsedUp = func() bool { return true }
	t.Cleanup(func() { codexUsedUp = usedUp })
	cx := codex(home)
	login := cx.Field("login")
	// a magpie model, so magpie is Codex's provider
	if err := cx.Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	keeps := func() bool {
		return strings.Contains(read(), "requires_openai_auth")
	}
	// the account mode: the provider needs OpenAI auth, so the app keeps
	// the account
	if err := login.Set("account"); err != nil {
		t.Fatal(err)
	}
	if !keeps() {
		t.Fatalf("the account mode does not keep the ChatGPT account:\n%s", read())
	}
	if cfg := read(); !strings.Contains(cfg, `model_provider = "magpie"`) {
		t.Fatalf("magpie is not Codex's provider:\n%s", cfg)
	}
	// the API mode: the app is in its API state, with no account
	if err := login.Set("api"); err != nil {
		t.Fatal(err)
	}
	if keeps() {
		t.Fatalf("the API mode keeps the account:\n%s", read())
	}
	// and back
	if err := login.Set("account"); err != nil {
		t.Fatal(err)
	}
	if !keeps() {
		t.Fatalf("the account mode does not keep the ChatGPT account:\n%s", read())
	}
}
