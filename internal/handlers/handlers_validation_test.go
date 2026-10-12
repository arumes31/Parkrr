package handlers

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPathIDLengthBounds(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		input string
		want  int64
		ok    bool
	}{
		{name: "positive", input: "1", want: 1, ok: true},
		{name: "int64 maximum", input: "9223372036854775807", want: 9223372036854775807, ok: true},
		{name: "signed int64 maximum", input: "+9223372036854775807", want: 9223372036854775807, ok: true},
		{name: "leading zeroes at cap", input: strings.Repeat("0", maxNumericParamLen-1) + "1", want: 1, ok: true},
		{name: "leading zeroes over cap", input: strings.Repeat("0", maxNumericParamLen) + "1"},
		{name: "overflow", input: "9223372036854775808"},
		{name: "empty"},
		{name: "zero", input: "0"},
		{name: "negative", input: "-1"},
		{name: "non numeric", input: "one"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest("GET", "/resource", nil)
			req.SetPathValue("id", tc.input)
			got, ok := pathID(req)
			if ok != tc.ok {
				t.Fatalf("pathID valid = %v, want %v", ok, tc.ok)
			}
			if ok && got != tc.want {
				t.Fatalf("pathID = %d, want %d", got, tc.want)
			}
		})
	}
}
