package vision

import (
	"reflect"
	"testing"
)

func TestParseGridSolution(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		wantTiles []int
		wantDone  string
		wantErr   bool
	}{
		{
			name:      "bare json",
			in:        `{"prompt":"select motorcycles","tiles":[1,5,9],"done":"verify","confidence":0.9}`,
			wantTiles: []int{1, 5, 9},
			wantDone:  "verify",
		},
		{
			name:      "fenced json with prose",
			in:        "Here is my answer:\n```json\n{\"tiles\":[2,3],\"done\":\"next\"}\n```\nDone.",
			wantTiles: []int{2, 3},
			wantDone:  "next",
		},
		{
			name:      "claude cli envelope",
			in:        `{"type":"result","subtype":"success","is_error":false,"result":"{\"tiles\":[4],\"prompt\":\"buses\"}"}`,
			wantTiles: []int{4},
			wantDone:  "verify", // defaulted
		},
		{
			name:      "no match empty tiles",
			in:        `{"tiles":[],"noMatch":true,"done":"verify"}`,
			wantTiles: []int{},
			wantDone:  "verify",
		},
		{
			name:    "no json",
			in:      "I cannot help with that.",
			wantErr: true,
		},
		{
			name:    "empty",
			in:      "   ",
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sol, err := parseGridSolution(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %+v", sol)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(tc.wantTiles) == 0 {
				if len(sol.Tiles) != 0 {
					t.Fatalf("tiles = %v, want empty", sol.Tiles)
				}
			} else if !reflect.DeepEqual(sol.Tiles, tc.wantTiles) {
				t.Fatalf("tiles = %v, want %v", sol.Tiles, tc.wantTiles)
			}
			if sol.Done != tc.wantDone {
				t.Fatalf("done = %q, want %q", sol.Done, tc.wantDone)
			}
		})
	}
}

func TestFirstJSONObject(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{`prefix {"a":1} suffix`, `{"a":1}`},
		{`{"a":{"b":2},"c":[1,2]}`, `{"a":{"b":2},"c":[1,2]}`},
		{`{"s":"a}b{c"}`, `{"s":"a}b{c"}`}, // braces inside string ignored
		{`no object here`, ``},
	}
	for _, tc := range cases {
		if got := firstJSONObject(tc.in); got != tc.want {
			t.Fatalf("firstJSONObject(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNewAPIModelRequiresKey(t *testing.T) {
	// The API transport requires a key; New() itself falls back to CLI when no
	// key is present (the no-API-key path), so we assert on newAPIModel directly.
	if _, err := newAPIModel("anthropic", Config{}); err == nil {
		t.Fatal("expected error when anthropic API transport built without a key")
	}
	if _, err := newAPIModel("anthropic", Config{APIKey: "k"}); err != nil {
		t.Fatalf("anthropic with key should build: %v", err)
	}
	if _, err := newAPIModel("openai", Config{APIKey: "k"}); err != nil {
		t.Fatalf("openai with key should build: %v", err)
	}
}
