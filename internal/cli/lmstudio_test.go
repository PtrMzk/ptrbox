package cli

// The real fetch, against a loopback server shaped like LM Studio's.

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// lmstudioServer serves canned bodies by path; a path with no body is a 404,
// which is what an older LM Studio says to /api/v0/models.
func lmstudioServer(t *testing.T, status int, bodies map[string]string) int {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := bodies[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().(*net.TCPAddr).Port
}

const nativeList = `{"object":"list","data":[
  {"id":"google/gemma-3-12b","type":"llm","state":"not-loaded","max_context_length":131072},
  {"id":"text-embedding-nomic-embed-text-v1.5","type":"embeddings","state":"not-loaded","max_context_length":2048},
  {"id":"qwen/qwen3-coder-30b","type":"llm","state":"loaded","max_context_length":262144,"loaded_context_length":32768},
  {"id":"qwen2-vl-7b","type":"vlm","state":"not-loaded","max_context_length":32768}]}`

// The native endpoint says what a model is and how much context it has; the
// loaded one sorts first with the window it is actually serving, and the
// embedding model is dropped by type rather than by name.
func TestFetchLMStudioModelsReadsTheNativeEndpoint(t *testing.T) {
	port := lmstudioServer(t, 200, map[string]string{"/api/v0/models": nativeList})
	got, err := fetchLMStudioModels(port)
	if err != nil {
		t.Fatal(err)
	}
	want := []lmModel{
		{ID: "qwen/qwen3-coder-30b", Context: 32768, Loaded: true},
		{ID: "google/gemma-3-12b", Context: 131072},
		{ID: "qwen2-vl-7b", Context: 32768},
	}
	if len(got) != len(want) {
		t.Fatalf("models = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("model %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// An LM Studio without the native endpoint still answers /v1/models: ids
// only, embedding models guessed by name, no context.
func TestFetchLMStudioModelsFallsBackToTheOpenAIList(t *testing.T) {
	port := lmstudioServer(t, 200, map[string]string{"/v1/models": `{"object":"list","data":[
		{"id":"qwen/qwen3-coder-30b","object":"model"},
		{"id":"text-embedding-nomic-embed-text-v1.5","object":"model"},
		{"id":"google/gemma-3-12b","object":"model"}]}`})
	got, err := fetchLMStudioModels(port)
	if err != nil {
		t.Fatal(err)
	}
	if want := "qwen/qwen3-coder-30b google/gemma-3-12b"; strings.Join(ids(got), " ") != want {
		t.Errorf("models = %+v, want %q (embedding model dropped, order kept)", got, want)
	}
	for _, m := range got {
		if m.Context != 0 || m.Loaded {
			t.Errorf("the fallback invented a context or a loaded state: %+v", m)
		}
	}
}

func ids(models []lmModel) []string {
	var out []string
	for _, m := range models {
		out = append(out, m.ID)
	}
	return out
}

func TestFetchLMStudioModelsFailsOnAnythingButAModelList(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"a server error": {500, "boom"},
		"not json":       {200, "<html>"},
	} {
		t.Run(name, func(t *testing.T) {
			port := lmstudioServer(t, tc.status, map[string]string{"/api/v0/models": tc.body, "/v1/models": tc.body})
			if _, err := fetchLMStudioModels(port); err == nil {
				t.Error("no error")
			}
		})
	}
}

func TestFetchLMStudioModelsNamesTheAddressWhenNothingListens(t *testing.T) {
	// Bind and release a port so nothing is on it.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	if _, err = fetchLMStudioModels(port); err == nil {
		t.Fatal("no error for a closed port")
	}
}

// The limits opencode compacts against: the served window, and an output cap
// that leaves most of it for the conversation. No window, no limit block -
// opencode is not told a number nobody measured.
func TestOpencodeModelsJSONCarriesTheLimitsItWasGiven(t *testing.T) {
	got := opencodeModelsJSON([]lmModel{
		{ID: "zeta", Context: 8192},
		{ID: "alpha@q4_k_m", Context: 131072},
		{ID: "bare"},
	})
	want := `{"alpha@q4_k_m":{"name":"alpha@q4_k_m","limit":{"context":131072,"output":8192}},` +
		`"bare":{"name":"bare"},` +
		`"zeta":{"name":"zeta","limit":{"context":8192,"output":2048}}}`
	if got != want {
		t.Errorf("json = %s\nwant   %s", got, want)
	}
	if got := opencodeModelsJSON(nil); got != "{}" {
		t.Errorf("json for no models = %s, want {}", got)
	}
}

func TestModelIDCharset(t *testing.T) {
	for _, ok := range []string{"qwen/qwen3-coder-30b", "gemma-3-12b@q4_k_m", "a.b:c_d", "Model7"} {
		if err := validModelID(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "-x", "a b", "a__b", `a"b`, "a$b", "a;b", "a\nb", "/abs"} {
		if err := validModelID(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
