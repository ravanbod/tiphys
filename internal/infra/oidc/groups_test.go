package oidc

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestGroupsFrom(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{"missing", ``, nil},
		{"array", `["/admins","devs"]`, []string{"/admins", "devs"}},
		{"single string", `"admins"`, []string{"admins"}},
		{"empty string", `""`, nil},
		{"wrong type", `{"a":1}`, nil},
		{"null", `null`, nil},
	}
	for _, c := range cases {
		if got := groupsFrom(json.RawMessage(c.raw)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
