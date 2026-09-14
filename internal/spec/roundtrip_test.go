package spec

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestRoundTrip_NoSilentDrops(t *testing.T) {
	s, err := parseEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var a, b map[string]any
	json.Unmarshal(s.Raw, &a)
	json.Unmarshal(out, &b)
	if !reflect.DeepEqual(a, b) {
		t.Errorf("round-trip changed spec:\nembedded: %s\nremarshalled: %s", s.Raw, out)
	}
}
