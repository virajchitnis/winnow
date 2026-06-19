package unsubscribe

import (
	"testing"

	"winnow/internal/store"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name       string
		header     string
		post       string
		wantMethod string
		wantTarget string
	}{
		{
			name:       "one-click preferred when post header present",
			header:     "<https://x.example/u?id=1>, <mailto:unsub@x.example>",
			post:       "List-Unsubscribe=One-Click",
			wantMethod: store.UnsubMethodOneClick,
			wantTarget: "https://x.example/u?id=1",
		},
		{
			name:       "mailto when no one-click",
			header:     "<https://x.example/u>, <mailto:unsub@x.example?subject=unsub>",
			post:       "",
			wantMethod: store.UnsubMethodMailto,
			wantTarget: "unsub@x.example?subject=unsub",
		},
		{
			name:       "bare https is manual",
			header:     "<https://x.example/u>",
			post:       "",
			wantMethod: store.UnsubMethodHTTP,
			wantTarget: "https://x.example/u",
		},
		{
			name:       "mailto only",
			header:     "<mailto:unsub@x.example>",
			post:       "",
			wantMethod: store.UnsubMethodMailto,
			wantTarget: "unsub@x.example",
		},
		{
			name:       "empty",
			header:     "",
			post:       "",
			wantMethod: "",
			wantTarget: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, target := Parse(tt.header, tt.post)
			if m != tt.wantMethod || target != tt.wantTarget {
				t.Errorf("Parse(%q,%q) = (%q,%q), want (%q,%q)",
					tt.header, tt.post, m, target, tt.wantMethod, tt.wantTarget)
			}
		})
	}
}
