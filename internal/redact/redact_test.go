package redact

import (
	"errors"
	"strings"
	"testing"
)

// All credentials below are synthetic.

func TestURL(t *testing.T) {
	cases := map[string]string{
		"http://127.0.0.1:8080/orders":                             "http://127.0.0.1:8080/orders",
		"http://user:pa55@127.0.0.1/x":                             "http://REDACTED@127.0.0.1/x",
		"https://ghp_SYNTHETICTOKEN@github.com/a/b":                "https://REDACTED@github.com/a/b",
		"http://h/orders?api_key=sk_test_SYNTH&amount=100":         "http://h/orders?amount=REDACTED&api_key=REDACTED",
		"http://h/cb?code=abc#access_token=SYNTH":                  "http://h/cb?code=REDACTED",
		"mailto:someone@example.com":                               "mailto:REDACTED",
		"http://h/_test/business-effect-count?key=order%201%26x=2": "http://h/_test/business-effect-count?key=REDACTED",
	}
	for in, want := range cases {
		if got := URL(in); got != want {
			t.Errorf("URL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestURL_Unparseable(t *testing.T) {
	if got := URL("http://h/%zz?token=SYNTH"); strings.Contains(got, "SYNTH") {
		t.Fatalf("unparseable URL leaked: %q", got)
	}
}

func TestText(t *testing.T) {
	cases := []struct {
		in       string
		secrets  []string
		mustKeep []string
	}{
		{
			in:       `step 0: Post "http://fguser:***@127.0.0.1:1/orders?api_key=sk_test_SYNTH": dial tcp 127.0.0.1:1: connect: connection refused`,
			secrets:  []string{"fguser", "sk_test_SYNTH"},
			mustKeep: []string{"step 0", "127.0.0.1:1/orders", "api_key=REDACTED", "connection refused"},
		},
		{
			in:       "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.SYNTH.SIG",
			secrets:  []string{"eyJhbGciOiJIUzI1NiJ9", "SYNTH"},
			mustKeep: []string{"Authorization: REDACTED"},
		},
		{
			in:       "upstream said: bearer abcDEF123== rejected",
			secrets:  []string{"abcDEF123"},
			mustKeep: []string{"rejected"},
		},
		{
			in:      `{"password":"hunter2-SYNTH","client_secret": "cs_SYNTH","X-Api-Key":"ak_SYNTH"}`,
			secrets: []string{"hunter2-SYNTH", "cs_SYNTH", "ak_SYNTH"},
		},
		{
			in:       "Cookie: session_id=SYNTHSESSION; theme=dark",
			secrets:  []string{"SYNTHSESSION"},
			mustKeep: []string{"theme=dark"},
		},
		{
			in:       "business_effect_count: expected 1, got 2",
			mustKeep: []string{"business_effect_count: expected 1, got 2"},
		},
		{
			in:       `host "localhost" is not in allowed_hosts`,
			mustKeep: []string{`host "localhost" is not in allowed_hosts`},
		},
	}

	for _, tc := range cases {
		got := Text(tc.in)
		for _, s := range tc.secrets {
			if strings.Contains(got, s) {
				t.Errorf("Text(%q) = %q, still contains %q", tc.in, got, s)
			}
		}
		for _, k := range tc.mustKeep {
			if !strings.Contains(got, k) {
				t.Errorf("Text(%q) = %q, lost diagnostic context %q", tc.in, got, k)
			}
		}
	}
}

func TestText_Idempotent(t *testing.T) {
	in := `Get "http://u:p@h/x?token=SYNTH": Authorization: Bearer SYNTH`
	once := Text(in)
	if twice := Text(once); twice != once {
		t.Fatalf("Text is not idempotent:\n once: %q\ntwice: %q", once, twice)
	}
}

func TestError(t *testing.T) {
	if Error(nil) != "" {
		t.Fatal("Error(nil) should be empty")
	}
	if got := Error(errors.New("token=SYNTH")); got != "token=REDACTED" {
		t.Fatalf("Error() = %q", got)
	}
}
