package cli

// LM Studio, asked at plan time.
//
// An opencode sandbox is only useful with a model server to talk to, and
// opencode cannot discover models on its own - its config wants them listed.
// So `ptrbox new` asks LM Studio on the Mac before building anything: which
// models it serves, so the guest's opencode.json can name them, and whether it
// is running at all, so a sandbox is never spent minutes on for a model server
// that is off. Both answers arrive as one HTTP request to the Mac's loopback,
// the same port the guest's firewall rule will be opened for.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// lmstudioModels asks LM Studio for its model ids. A variable so the test
// harness can answer for a Mac with, or without, LM Studio running.
var lmstudioModels = fetchLMStudioModels

// fetchLMStudioModels GETs the OpenAI-compatible model list on the Mac's
// loopback. Embedding models are dropped: they answer /v1/models like any
// other but cannot hold a conversation, and "embed" in the id is how LM Studio
// names them. A heuristic, and marked as one.
func fetchLMStudioModels(port int) ([]string, error) {
	url := fmt.Sprintf("http://127.0.0.1:%d/v1/models", port)
	// No proxy, whatever the environment says: this is the Mac asking itself.
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s", url, resp.Status)
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("%s did not answer with a model list: %v", url, err)
	}
	var ids []string
	for _, model := range body.Data {
		if strings.Contains(model.ID, "embed") {
			continue
		}
		ids = append(ids, model.ID)
	}
	return ids, nil
}

// modelIDRe is the charset a model id may have before it is rendered into the
// guest. LM Studio ids look like `qwen/qwen3-coder-30b` or
// `gemma-3-12b@q4_k_m`; anything outside this is refused rather than escaped,
// the NODE_VERSION argument. A double underscore is refused separately because
// it is the render's placeholder pattern.
var modelIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@-]*$`)

// validModelID says whether an id can be rendered as itself.
func validModelID(id string) error {
	if !modelIDRe.MatchString(id) || strings.Contains(id, "__") {
		return fmt.Errorf("LM Studio model id %q cannot be rendered into the sandbox (letters, digits, . _ : / @ - only)", id)
	}
	return nil
}

// opencodeModelsJSON is opencode.json's `models` object for these ids: one
// line, keys sorted, each model displayed under its own id. Built here with
// encoding/json rather than assembled in the guest so the guest never
// interpolates anything - the render substitutes it as one token.
func opencodeModelsJSON(ids []string) string {
	models := map[string]struct {
		Name string `json:"name"`
	}{}
	for _, id := range ids {
		models[id] = struct {
			Name string `json:"name"`
		}{Name: id}
	}
	body, err := json.Marshal(models)
	if err != nil {
		// A map of strings cannot fail to marshal; this is belt and braces.
		return "{}"
	}
	return string(body)
}
