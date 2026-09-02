package web

import "testing"

func TestWSOrigin_Patterns(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		baseURL string
		want    []string
	}{
		{name: "empty", baseURL: "", want: nil},
		{name: "whitespace", baseURL: "  ", want: nil},
		{name: "not a url", baseURL: "not a url", want: nil},
		{name: "public https", baseURL: "https://staging.guest-pass.link", want: []string{"staging.guest-pass.link"}},
		{name: "public with port", baseURL: "https://gp.example:8443", want: []string{"gp.example:8443"}},
		{name: "trimmed", baseURL: "  https://gp.example  ", want: []string{"gp.example"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := WSOrigin{}.Patterns(tc.baseURL)
			if len(got) != len(tc.want) {
				t.Fatalf("Patterns(%q) = %#v, want %#v", tc.baseURL, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("Patterns(%q) = %#v, want %#v", tc.baseURL, got, tc.want)
				}
			}
		})
	}
}

func TestWSOrigin_AcceptOptionsUsesPatterns(t *testing.T) {
	t.Parallel()
	opts := WSOrigin{}.AcceptOptions("https://gp.example")
	if opts == nil || len(opts.OriginPatterns) != 1 || opts.OriginPatterns[0] != "gp.example" {
		t.Fatalf("AcceptOptions patterns = %#v, want [gp.example]", opts)
	}
	if opts.InsecureSkipVerify {
		t.Fatal("AcceptOptions must not set InsecureSkipVerify")
	}
}
