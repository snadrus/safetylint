package nolint

import (
	"reflect"
	"testing"
)

func TestParseNolint(t *testing.T) {
	tests := []struct {
		text string
		want []string
	}{
		{`//nolint:safetylint // bla bla bla`, []string{"safetylint"}},
		{`//nolint:safetylint`, []string{"safetylint"}},
		{`//nolint:nosharing`, []string{"nosharing"}},
		{`//nolint:nounsafe`, []string{"nounsafe"}},
		{`//nolint:safetylint,other`, []string{"safetylint", "other"}},
		{`//nolint:nosharing,safetylint`, []string{"nosharing", "safetylint"}},
		{`// nolint:safetylint`, []string{"safetylint"}},
		{`//nolint: nosharing , nounsafe`, []string{"nosharing", "nounsafe"}},
		{`//nolint`, nil},
		{`//nolint // reason`, nil},
		{`// something else`, nil},
		{`//want nosharing`, nil},
	}
	for _, tt := range tests {
		got := parseNolint(tt.text)
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("parseNolint(%q) = %v, want %v", tt.text, got, tt.want)
		}
	}
}

func TestCommentHas(t *testing.T) {
	if !commentHas(`//nolint:safetylint // reason`, "nosharing") {
		t.Fatal("umbrella should suppress nosharing")
	}
	if !commentHas(`//nolint:safetylint`, "nounsafe") {
		t.Fatal("umbrella should suppress nounsafe")
	}
	if commentHas(`//nolint:nosharing`, "nounsafe") {
		t.Fatal("nosharing must not suppress nounsafe")
	}
	if commentHas(`//nolint:nounsafe`, "nosharing") {
		t.Fatal("nounsafe must not suppress nosharing")
	}
	if commentHas(`//nolint`, "nosharing") {
		t.Fatal("bare nolint must not suppress")
	}
}
