// Param presets (Open WebUI parity): the Params drawer persists a small JSON
// array under the me-settings key "presets". This file pins the server-side
// contract: the key is accepted, well-formed arrays round-trip, and malformed
// or oversized payloads 400 BEFORE any write. Entry payload beyond id/name is
// deliberately opaque (validateMePresets) — sampling params are validated at
// USE by the chat PATCH path, not duplicated here.
package apimgr

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestValidateMePresets(t *testing.T) {
	ok := []string{
		`[]`,
		`[{"id":"p1","name":"Code","model":"p1:m1","system":"You code.","params":{"temperature":0.2}}]`,
		`[{"id":"p1","name":"A"},{"id":"p2","name":"B"}]`,
	}
	for _, v := range ok {
		if msg := validateMePresets(v); msg != "" {
			t.Errorf("validateMePresets(%q) = %q, want accept", v, msg)
		}
	}

	bad := map[string]string{
		"":                        "empty",
		`{"id":"p1","name":"A"}`:  "object not array",
		`not json`:                "garbage",
		`[{"name":"no id"}]`:      "missing id",
		`[{"id":"p1"}]`:           "missing name",
		`[{"id":" ","name":"A"}]`: "blank id",
		strings.Repeat("字", 65):   "over-long non-array",
		`[` + strings.TrimSuffix(strings.Repeat(`{"id":"p","name":"n"},`, maxMePresets+1), ",") + `]`: "over count",
	}
	for v, why := range bad {
		if msg := validateMePresets(v); msg == "" {
			t.Errorf("validateMePresets(%q) accepted (%s), want refuse", v, why)
		}
	}
}

// Wire round-trip: the "presets" key must pass the meSettingKeys allowlist
// (unknown keys hard-400) and come back byte-identical from GET.
func TestMePresetsRoundTripAndRefusals(t *testing.T) {
	e := newMeNameEnv(t)

	payload := `[{"id":"p1","name":"Code","model":"p1:m1","system":"stay terse","params":{"temperature":0.2}}]`
	rec := e.do(t, http.MethodPut, "/api/me/settings", `{"presets":`+encodeJSONString(payload)+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT presets = %d, want 200 (body %s)", rec.Code, rec.Body)
	}

	rec = e.do(t, http.MethodGet, "/api/me/settings", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET me/settings = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "stay terse") {
		t.Fatalf("GET me/settings lost the presets payload: %s", rec.Body)
	}

	rec = e.do(t, http.MethodPut, "/api/me/settings", `{"presets":"[{\\"name\\":\\"missing id\\"}]"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT malformed presets = %d, want 400 (body %s)", rec.Code, rec.Body)
	}
}

// encodeJSONString quotes a string for embedding as a JSON value.
func encodeJSONString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}
