package main

import "testing"

func TestSubscriptionFromJSON(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "active cline pass plan",
			body: `{"success":true,"data":{"plan":{"name":"cline_pass","displayName":"ClinePass","isActive":true},"entitlements":{"cline_pass":{"enabled":true}}}}`,
			want: "pass",
		},
		{
			name: "free plan",
			body: `{"success":true,"data":{"plan":{"name":"free","type":"free","isActive":true}}}`,
			want: "free",
		},
		{
			name: "successful empty plan means free",
			body: `null`,
			want: "free",
		},
		{
			name: "unrelated response stays unknown",
			body: `{"success":true,"data":{"user":{"email":"passenger@example.com"},"models":[{"id":"free-model"}]}}`,
			want: "unknown",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := subscriptionFromJSON([]byte(tt.body)); got != tt.want {
				t.Fatalf("subscriptionFromJSON() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRequestedSubscriptionPrefersExplicitChoice(t *testing.T) {
	if got := requestedSubscription("free", "pass"); got != "free" {
		t.Fatalf("explicit Free choice was overridden: %q", got)
	}
	if got := requestedSubscription("unknown", "pass"); got != "pass" {
		t.Fatalf("detected Pass was not adopted: %q", got)
	}
	if got := requestedSubscription("", ""); got != subscriptionUnknown {
		t.Fatalf("missing subscription should remain unknown: %q", got)
	}
}
