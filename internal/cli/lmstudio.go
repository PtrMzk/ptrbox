package cli

// LM Studio, asked at plan time.
//
// An opencode sandbox is only useful with a model server to talk to, and
// opencode cannot discover models on its own - its config wants them listed,
// with their context limits if it is to compact a conversation before the
// server truncates it. So `ptrbox new` asks LM Studio on the Mac before
// building anything: which chat models it serves and how much context each
// has, so the guest's opencode.json can name them; and whether it is running
// at all, so a sandbox is never spent minutes on for a model server that is
// off. One HTTP request to the Mac's loopback, the same port the guest's
// firewall rule will be opened for.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
)

// lmModel is what the plan learns about one chat model.
type lmModel struct {
	ID string
	// Context is the window the server will actually serve: the loaded
	// length when the model is loaded, the model's maximum otherwise. Zero
	// when the server could not say, in which case opencode gets no limit.
	Context int
	Loaded  bool
}

// lmstudioModels asks LM Studio for its chat models. A variable so the test
// harness can answer for a Mac with, or without, LM Studio running.
var lmstudioModels = fetchLMStudioModels

// fetchLMStudioModels prefers LM Studio's native endpoint, which says what a
// model IS (embedding models answer /v1/models like any other and cannot hold
// a conversation) and how much context it has; an older LM Studio without it
// falls back to the OpenAI-compatible list, with a substring heuristic for
// the first question and no answer to the second.
//
// Loaded models sort first: the default opencode gets is the first entry,
// and the model already in memory is the one whose context length is real
// and whose first request costs no load.
func fetchLMStudioModels(port int) ([]lmModel, error) {
	models, err := fetchNative(port)
	if err != nil {
		var v1err error
		if models, v1err = fetchOpenAI(port); v1err != nil {
			return nil, v1err
		}
	}
	slices.SortStableFunc(models, func(a, b lmModel) int {
		switch {
		case a.Loaded && !b.Loaded:
			return -1
		case b.Loaded && !a.Loaded:
			return 1
		}
		return 0
	})
	return models, nil
}

// loopbackClient never uses a proxy, whatever the environment says: this is
// the Mac asking itself.
func loopbackClient() *http.Client {
	return &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}
}

func getJSON(url string, into any) error {
	resp, err := loopbackClient().Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %s", url, resp.Status)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(into); err != nil {
		return fmt.Errorf("%s did not answer with a model list: %v", url, err)
	}
	return nil
}

// fetchNative reads /api/v0/models: type, state and both context lengths.
func fetchNative(port int) ([]lmModel, error) {
	var body struct {
		Data []struct {
			ID     string `json:"id"`
			Type   string `json:"type"`
			State  string `json:"state"`
			Max    int    `json:"max_context_length"`
			Loaded int    `json:"loaded_context_length"`
		} `json:"data"`
	}
	if err := getJSON(fmt.Sprintf("http://127.0.0.1:%d/api/v0/models", port), &body); err != nil {
		return nil, err
	}
	var models []lmModel
	for _, m := range body.Data {
		if m.Type == "embeddings" {
			continue
		}
		model := lmModel{ID: m.ID, Context: m.Max, Loaded: m.State == "loaded"}
		if m.Loaded > 0 {
			model.Context = m.Loaded
		}
		models = append(models, model)
	}
	return models, nil
}

// fetchOpenAI reads /v1/models, which every LM Studio serves and which says
// only ids. "embed" in the id is how LM Studio names its embedding models - a
// heuristic, and only reached when the native endpoint is not there.
func fetchOpenAI(port int) ([]lmModel, error) {
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := getJSON(fmt.Sprintf("http://127.0.0.1:%d/v1/models", port), &body); err != nil {
		return nil, err
	}
	var models []lmModel
	for _, m := range body.Data {
		if strings.Contains(m.ID, "embed") {
			continue
		}
		models = append(models, lmModel{ID: m.ID})
	}
	return models, nil
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

// maxOutput is the largest single answer opencode may ask for. A quarter of
// the window, capped: a coding turn rarely needs more, and leaving most of the
// window for the conversation is what keeps prefill served from the cache.
const maxOutput = 8192

// opencodeModelsJSON is opencode.json's `models` object for these models: one
// line, keys sorted, each model displayed under its own id, with its limits
// when the server said what they are. Built here with encoding/json rather
// than assembled in the guest so the guest never interpolates anything - the
// render substitutes it as one token.
func opencodeModelsJSON(models []lmModel) string {
	type limit struct {
		Context int `json:"context"`
		Output  int `json:"output"`
	}
	type entry struct {
		Name  string `json:"name"`
		Limit *limit `json:"limit,omitempty"`
	}
	out := map[string]entry{}
	for _, m := range models {
		e := entry{Name: m.ID}
		if m.Context > 0 {
			e.Limit = &limit{Context: m.Context, Output: min(maxOutput, m.Context/4)}
		}
		out[m.ID] = e
	}
	body, err := json.Marshal(out)
	if err != nil {
		// A map of strings and ints cannot fail to marshal; belt and braces.
		return "{}"
	}
	return string(body)
}

// describe is one model as the plan names it: id, context, and whether it is
// already in memory.
func (m lmModel) describe() string {
	s := m.ID
	if m.Context > 0 {
		s += fmt.Sprintf(" (%dk)", m.Context/1024)
	}
	if m.Loaded {
		s += " loaded"
	}
	return s
}
