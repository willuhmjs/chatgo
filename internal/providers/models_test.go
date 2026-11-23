package providers

import (
	"testing"
)

func TestParseModelListVariants(t *testing.T) {
	// Wrapped shape with duplicates, missing fields and unknown extras.
	body := `{"object":"list","data":[
		{"id":"gpt-4o","object":"model","created":1,"display_name":"GPT-4o","owned_by":"openai"},
		{"id":"gpt-4-32k","object":"model"},
		{"id":"gpt-4o","object":"model","display_name":"dup ignored"},
		{"id":"","object":"model"},
		{"id":"vllm/llama-3.1-8b-instruct-128k","max_model_len":131072,"weird":{"nested":[1,2]}}
	]}`
	got, err := parseModelList([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	want := []Model{
		{ID: "gpt-4o", Display: "GPT-4o", ContextLen: 0},
		{ID: "gpt-4-32k", Display: "Gpt 4 32k", ContextLen: 32000},
		{ID: "vllm/llama-3.1-8b-instruct-128k", Display: "Llama 3 1 8b instruct 128k", ContextLen: 131072},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("model %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestParseModelListBareArray(t *testing.T) {
	got, err := parseModelList([]byte(`[{"id":"m1","name":"Model One"},{"id":"m2"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Display != "Model One" || got[1].Display != "M2" {
		t.Fatalf("got %+v", got)
	}
}

func TestParseModelListEmptyAndGarbage(t *testing.T) {
	if got, err := parseModelList([]byte(`{"data":[]}`)); err != nil || len(got) != 0 {
		t.Fatalf(`{"data":[]}: got %v, %v`, got, err)
	}
	if _, err := parseModelList([]byte(`<html>502 bad gateway</html>`)); err == nil {
		t.Fatal("expected error for garbage body")
	}
}

func TestGuessContextLen(t *testing.T) {
	for _, tc := range []struct {
		id   string
		want int
	}{
		{"gpt-4-32k", 32000},
		{"gpt-4-32k-0613", 32000},
		{"gpt-4o-2024-08-06", 0}, // dates must not match
		{"gpt-4o-128k", 128000},
		{"gpt-4.1-2025-04-14", 0}, // no hint
		{"llama-3.1-8b-instruct-128k", 128000},
		{"mistral-nemo-128K", 128000}, // case-insensitive
		{"qwen2.5-coder-1m", 1000000},
		{"kimi-k2", 0},
		{"llama2-70b", 0}, // size class, not context
		{"mixtral-8x7b-instruct-v0.1", 0},
		{"claude-3-5-sonnet-200k", 200000},
		{"gpt-3.5-turbo", 0},
		{"whisper-1", 0},
		{"text-embedding-3-small", 0},
	} {
		if got := guessContextLen(tc.id); got != tc.want {
			t.Errorf("guessContextLen(%q) = %d, want %d", tc.id, got, tc.want)
		}
	}
}

func TestHumanizeModelID(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"openai/gpt-4o-mini", "Gpt 4o mini"},
		{"meta_llama3-70b", "Meta llama3 70b"},
		{"x", "X"},
		{"/", "/"},
	} {
		if got := humanizeModelID(tc.in); got != tc.want {
			t.Errorf("humanizeModelID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
