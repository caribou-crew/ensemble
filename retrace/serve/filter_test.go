package serve

import (
	"net/url"
	"testing"

	"github.com/caribou-crew/ensemble/retrace/runs"
)

func TestQueueFilter_Apply(t *testing.T) {
	ci := &runs.Source{Kind: "ci", Workflow: "Retrace web"}
	items := []Item{
		{App: "web", Flow: "cart"},                    // local
		{App: "ios-native", Flow: "cart", Source: ci}, // ci
		{App: "android-rn", Flow: "cart"},             // local
	}

	tests := []struct {
		name  string
		query string
		want  []string // expected apps, in order
	}{
		{"unfiltered", "", []string{"web", "ios-native", "android-rn"}},
		{"local only", "source=local", []string{"web", "android-rn"}},
		{"ci only", "source=ci", []string{"ios-native"}},
		{"exact app", "app=web", []string{"web"}},
		{"source and app compose", "source=local&app=android-rn", []string{"android-rn"}},
		{"typo narrows to empty", "source=bogus", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q, _ := url.ParseQuery(tc.query)
			got := QueueFilterFromQuery(q).Apply(items)
			var apps []string
			for _, it := range got {
				apps = append(apps, it.App)
			}
			if len(apps) != len(tc.want) {
				t.Fatalf("query %q: got %v, want %v", tc.query, apps, tc.want)
			}
			for i := range apps {
				if apps[i] != tc.want[i] {
					t.Fatalf("query %q: got %v, want %v", tc.query, apps, tc.want)
				}
			}
			// Apply never returns nil.
			if got == nil {
				t.Errorf("query %q: Apply returned nil, want non-nil (possibly empty) slice", tc.query)
			}
		})
	}
}
