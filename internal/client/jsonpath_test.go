package client

import (
	"encoding/json"
	"testing"
)

func TestJSONPathLookup(t *testing.T) {
	var doc any
	_ = json.Unmarshal([]byte(`{"d":{"results":[{"MessageGuid":"AAA","n":1},{"MessageGuid":"BBB"}],"count":2},"matrix":[[1,2],[3,4]],"a.b":"dotted"}`), &doc)
	cases := []struct {
		path string
		want any
		ok   bool
	}{
		{"d.count", float64(2), true},
		{"d.results.0.MessageGuid", "AAA", true},  // bare numeric index
		{"d.results[1].MessageGuid", "BBB", true}, // bracket index
		{"matrix[1][0]", float64(3), true},        // chained indexes
		{"matrix.1.1", float64(4), true},
		{"d.results.5.MessageGuid", nil, false}, // out of range
		{"d.results[-1]", nil, false},
		{"d.results[x]", nil, false},
		{"d.results[0", nil, false},
		{"d.results.MessageGuid", nil, false}, // a key on an array
		{"d.missing", nil, false},
		{"d.count.deeper", nil, false},
	}
	for _, c := range cases {
		got, ok := jsonPathLookup(doc, c.path)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("%q = %v, %v; want %v, %v", c.path, got, ok, c.want, c.ok)
		}
	}
}
