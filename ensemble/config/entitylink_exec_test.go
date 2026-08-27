package config

import (
	"strings"
	"testing"
)

// --- Validate() table tests for kind: exec entity links. ---

func TestValidateEntityLinkKindExec(t *testing.T) {
	cases := []struct {
		name    string
		link    EntityLink
		wantErr []string // substrings that must all appear in the error
	}{
		{
			name: "valid exec link",
			link: EntityLink{Label: "Open on Android", Template: "myapp://widget/{{id}}", Kind: "exec", Exec: "adb-view"},
		},
		{
			name: "valid url link (kind absent)",
			link: EntityLink{Label: "Open in web app", Template: "https://app.example.test/{{id}}"},
		},
		{
			name: "valid url link (kind explicit)",
			link: EntityLink{Label: "Open in web app", Template: "https://app.example.test/{{id}}", Kind: "url"},
		},
		{
			name:    "unknown kind",
			link:    EntityLink{Label: "L", Template: "https://x/{{id}}", Kind: "download"},
			wantErr: []string{"users", "link 0", `kind "download"`},
		},
		{
			name:    "url link sets exec",
			link:    EntityLink{Label: "L", Template: "https://x/{{id}}", Kind: "url", Exec: "adb-view"},
			wantErr: []string{"users", "link 0", "exec is set but kind is"},
		},
		{
			name:    "default-kind link sets exec",
			link:    EntityLink{Label: "L", Template: "https://x/{{id}}", Exec: "adb-view"},
			wantErr: []string{"exec is set but kind is"},
		},
		{
			name:    "exec link missing exec name",
			link:    EntityLink{Label: "L", Template: "myapp://{{id}}", Kind: "exec"},
			wantErr: []string{"users", "link 0", "kind: exec requires exec:", "adb-view", "ios-simctl-openurl"},
		},
		{
			name:    "exec link unknown exec name",
			link:    EntityLink{Label: "L", Template: "myapp://{{id}}", Kind: "exec", Exec: "adb-veiw"},
			wantErr: []string{"users", "link 0", `exec "adb-veiw" is not a known command`, "adb-view", "ios-simctl-openurl"},
		},
		{
			name:    "exec link scheme sourced from a column",
			link:    EntityLink{Label: "L", Template: "{{scheme}}://widget/{{id}}", Kind: "exec", Exec: "adb-view"},
			wantErr: []string{"users", "link 0", "literal scheme"},
		},
		{
			name:    "exec link with no scheme at all",
			link:    EntityLink{Label: "L", Template: "{{id}}", Kind: "exec", Exec: "adb-view"},
			wantErr: []string{"literal scheme"},
		},
		{
			name: "exec link with literal scheme immediately followed by a placeholder",
			link: EntityLink{Label: "L", Template: "myapp:{{id}}", Kind: "exec", Exec: "adb-view"},
		},
		{
			name:    "exec link template contains a control character",
			link:    EntityLink{Label: "L", Template: "myapp://widget/\n{{id}}", Kind: "exec", Exec: "adb-view"},
			wantErr: []string{"users", "link 0", "control character"},
		},
		{
			name: "exec link with reverse referencing a real service",
			link: EntityLink{Label: "L", Template: "myapp://widget/{{id}}", Kind: "exec", Exec: "adb-view", Reverse: []string{"auth"}},
		},
		{
			name:    "url link sets reverse",
			link:    EntityLink{Label: "L", Template: "https://x/{{id}}", Kind: "url", Reverse: []string{"auth"}},
			wantErr: []string{"users", "link 0", "reverse is set but kind is"},
		},
		{
			name:    "reverse references unknown service",
			link:    EntityLink{Label: "L", Template: "myapp://{{id}}", Kind: "exec", Exec: "adb-view", Reverse: []string{"does-not-exist"}},
			wantErr: []string{"users", "link 0", `reverse references unknown/unroutable service/stub/gateway "does-not-exist"`},
		},
		{
			name:    "reverse set on a command that doesn't support it",
			link:    EntityLink{Label: "L", Template: "myapp://{{id}}", Kind: "exec", Exec: "ios-simctl-openurl", Reverse: []string{"auth"}},
			wantErr: []string{"users", "link 0", `reverse is set but exec "ios-simctl-openurl" does not support it`},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{
				Services: map[string]Service{"auth": {Port: 9001, Run: "true"}},
				Entities: map[string]Entity{
					"users": {Base: "http://x", ID: "id", Links: []EntityLink{tc.link}},
				},
			}
			err := c.Validate()
			if len(tc.wantErr) == 0 {
				if err != nil {
					t.Fatalf("expected no error, got: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			for _, substr := range tc.wantErr {
				if !strings.Contains(err.Error(), substr) {
					t.Errorf("error %q does not contain %q", err.Error(), substr)
				}
			}
		})
	}
}

func TestValidateEntityLinkDuplicateLabel(t *testing.T) {
	c := &Config{Entities: map[string]Entity{
		"users": {Base: "http://x", ID: "id", Links: []EntityLink{
			{Label: "Open", Template: "https://a/{{id}}"},
			{Label: "Open", Template: "https://b/{{id}}"},
		}},
	}}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "users") || !strings.Contains(err.Error(), `duplicate label "Open"`) {
		t.Errorf("error does not name the entity/duplicate label: %v", err)
	}
}

func TestValidateEntityLinkDistinctLabelsOK(t *testing.T) {
	c := &Config{Entities: map[string]Entity{
		"users": {Base: "http://x", ID: "id", Links: []EntityLink{
			{Label: "Open A", Template: "https://a/{{id}}"},
			{Label: "Open B", Template: "https://b/{{id}}"},
		}},
	}}
	if err := c.Validate(); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}
