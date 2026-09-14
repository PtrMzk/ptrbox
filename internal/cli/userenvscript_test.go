package cli

// vm/provision/40-userenv.sh, run for real - the fifth guest script the suite
// executes rather than lints, and the first of the user-side ones.
//
// What is worth executing: the opencode branch writes a config file and a
// block of exports, and both are the kind of thing that fails silently. A
// comma slipped in the JSON is a config opencode discards without a word; an
// export with the wrong shape is one vm/verify.sh's credential check refuses.
// So the file is parsed back as JSON, the exports are read back by verify.sh
// itself, and the branch is shown to be inert with the flag off.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	ptrbox "github.com/PtrMzk/ptrbox"
	"github.com/PtrMzk/ptrbox/internal/render"
)

// userenvScript renders 40-userenv.sh for a sandbox with or without opencode
// and gives it a HOME of its own.
func userenvScript(t *testing.T, opencode bool) (dir string) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not available")
	}
	dir = t.TempDir()
	values := render.Values{
		"VM_NAME": "demo", "VM_COLOR": "1;32",
		"PROXY_HOST": "192.168.5.2", "PROXY_PORT": "8889",
		// Empty on purpose: a set identity would run git against the
		// developer's global config.
		"GIT_USER_NAME": "", "GIT_USER_EMAIL": "",
		"CLAUDE_MODEL":         "opus",
		"OPENCODE":             "false",
		"LMSTUDIO_PORT":        "1234",
		"OPENCODE_MODELS_JSON": "{}",
		"OPENCODE_MODEL":       "",
	}
	if opencode {
		values["OPENCODE"] = "true"
		values["OPENCODE_MODELS_JSON"] = `{"google/gemma-3-12b":{"name":"google/gemma-3-12b"},"qwen/qwen3-coder-30b":{"name":"qwen/qwen3-coder-30b"}}`
		values["OPENCODE_MODEL"] = "qwen/qwen3-coder-30b"
	}
	var buf strings.Builder
	if err := render.Render(&buf, ptrbox.Assets, "vm/provision/40-userenv.sh", "vm", values); err != nil {
		t.Fatal(err)
	}
	writeScript(t, filepath.Join(dir, "userenv.sh"), buf.String())
	writeScript(t, filepath.Join(dir, "verify.sh"), asset(t, "vm/verify.sh"))

	stubs := sharedStubs(t, "quiet", quietStubs)
	t.Setenv("PATH", stubs+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", dir)
	return dir
}

func runUserenv(t *testing.T, dir string) {
	t.Helper()
	if out, err := exec.Command("bash", filepath.Join(dir, "userenv.sh")).CombinedOutput(); err != nil {
		t.Fatalf("40-userenv.sh failed: %v\n%s", err, out)
	}
}

func TestOpencodeConfigIsValidJSONPointedAtLMStudioWithNoKey(t *testing.T) {
	dir := userenvScript(t, true)
	runUserenv(t, dir)

	body, err := os.ReadFile(filepath.Join(dir, ".config", "opencode", "opencode.json"))
	if err != nil {
		t.Fatalf("no opencode.json: %v", err)
	}
	var cfg struct {
		Autoupdate        bool     `json:"autoupdate"`
		Share             string   `json:"share"`
		Model             string   `json:"model"`
		SmallModel        string   `json:"small_model"`
		DisabledProviders []string `json:"disabled_providers"`
		Provider          map[string]struct {
			NPM     string            `json:"npm"`
			Options map[string]string `json:"options"`
			Models  map[string]struct {
				Name string `json:"name"`
			} `json:"models"`
		} `json:"provider"`
	}
	if err := json.Unmarshal(body, &cfg); err != nil {
		t.Fatalf("opencode.json does not parse (opencode would discard it silently): %v\n%s", err, body)
	}
	if cfg.Autoupdate || cfg.Share != "disabled" {
		t.Errorf("autoupdate=%v share=%q, want off and disabled", cfg.Autoupdate, cfg.Share)
	}
	lm, ok := cfg.Provider["lmstudio"]
	if !ok {
		t.Fatalf("no lmstudio provider in %s", body)
	}
	if lm.NPM != "@ai-sdk/openai-compatible" || lm.Options["baseURL"] != "http://192.168.5.2:1234/v1" {
		t.Errorf("provider = %+v, want the openai-compatible package pointed at the gateway", lm)
	}
	if len(lm.Models) != 2 || lm.Models["qwen/qwen3-coder-30b"].Name != "qwen/qwen3-coder-30b" {
		t.Errorf("models = %+v", lm.Models)
	}
	if cfg.Model != "lmstudio/qwen/qwen3-coder-30b" {
		t.Errorf("default model = %q", cfg.Model)
	}
	// The side-task model too: opencode's default for it prefers its own
	// hosted provider, which is session text leaving for opencode's servers.
	if cfg.SmallModel != cfg.Model {
		t.Errorf("small_model = %q, want the local model %q", cfg.SmallModel, cfg.Model)
	}
	if len(cfg.DisabledProviders) != 1 || cfg.DisabledProviders[0] != "opencode" {
		t.Errorf("disabled_providers = %v, want the hosted opencode provider off", cfg.DisabledProviders)
	}
	// No key anywhere in the document, under any spelling.
	var anything map[string]any
	json.Unmarshal(body, &anything)
	walkKeys(anything, func(key string) {
		if strings.Contains(strings.ToLower(key), "key") {
			t.Errorf("opencode.json carries a key-shaped field %q", key)
		}
	})

	url, _ := os.ReadFile(filepath.Join(dir, ".ptrbox", "lmstudio-url"))
	if strings.TrimSpace(string(url)) != "http://192.168.5.2:1234" {
		t.Errorf("lmstudio-url = %q", url)
	}
}

// walkKeys visits every object key in a decoded JSON document.
func walkKeys(v any, visit func(string)) {
	switch node := v.(type) {
	case map[string]any:
		for k, child := range node {
			visit(k)
			walkKeys(child, visit)
		}
	case []any:
		for _, child := range node {
			walkKeys(child, visit)
		}
	}
}

// The phone-home switches are exports in ~/.profile, and ~/.profile is what
// verify.sh's credential check reads - so the two have to agree that none of
// them is key-shaped. Asked of verify.sh itself rather than of a regex copy.
func TestOpencodePhoneHomeIsOffAndPassesTheCredentialCheck(t *testing.T) {
	dir := userenvScript(t, true)
	runUserenv(t, dir)

	profile, _ := os.ReadFile(filepath.Join(dir, ".profile"))
	for _, want := range []string{
		"export OPENCODE_DISABLE_AUTOUPDATE=1",
		"export OPENCODE_DISABLE_SHARE=1",
		"export OPENCODE_DISABLE_LSP_DOWNLOAD=1",
		"export OPENCODE_DISABLE_MODELS_FETCH=1",
		`export NO_PROXY="localhost,127.0.0.1,192.168.5.2"`,
	} {
		if !strings.Contains(string(profile), want) {
			t.Errorf(".profile lacks %s", want)
		}
	}
	if line := verifyLine(t, dir, filepath.Join(dir, "state"), "one credential only"); !strings.Contains(line, "OK") {
		t.Errorf("verify.sh = %q, want OK - an export tripped the credential check", line)
	}
}

func TestWithoutOpencodeTheBranchIsInert(t *testing.T) {
	dir := userenvScript(t, false)
	runUserenv(t, dir)
	for _, absent := range []string{
		filepath.Join(".config", "opencode", "opencode.json"),
		filepath.Join(".ptrbox", "lmstudio-url"),
	} {
		if _, err := os.Stat(filepath.Join(dir, absent)); err == nil {
			t.Errorf("%s was written for a sandbox without opencode", absent)
		}
	}
	profile, _ := os.ReadFile(filepath.Join(dir, ".profile"))
	if strings.Contains(string(profile), "OPENCODE_DISABLE") {
		t.Error("opencode's exports reached a sandbox without opencode")
	}
	// Claude Code's own pre-seed is untouched by the branch.
	if _, err := os.Stat(filepath.Join(dir, ".claude", "settings.json")); err != nil {
		t.Error("the Claude settings pre-seed is missing")
	}
}

// Done-guarded like every provision script: a later boot changes nothing.
func TestUserenvSecondRunIsInert(t *testing.T) {
	dir := userenvScript(t, true)
	runUserenv(t, dir)
	before, _ := os.ReadFile(filepath.Join(dir, ".profile"))
	runUserenv(t, dir)
	after, _ := os.ReadFile(filepath.Join(dir, ".profile"))
	if string(before) != string(after) {
		t.Error("the second run appended to ~/.profile again")
	}
}
