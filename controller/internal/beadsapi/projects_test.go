package beadsapi

import "testing"

func TestRoleForChannel(t *testing.T) {
	tests := []struct {
		name         string
		channelRoles map[string]string
		channelID    string
		want         string
	}{
		{
			name:         "nil map returns empty",
			channelRoles: nil,
			channelID:    "C123",
			want:         "",
		},
		{
			name:         "empty map returns empty",
			channelRoles: map[string]string{},
			channelID:    "C123",
			want:         "",
		},
		{
			name:         "matching channel returns role",
			channelRoles: map[string]string{"C123": "k6-perf", "C456": "crew"},
			channelID:    "C123",
			want:         "k6-perf",
		},
		{
			name:         "non-matching channel returns empty",
			channelRoles: map[string]string{"C123": "k6-perf"},
			channelID:    "C999",
			want:         "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := ProjectInfo{ChannelRoles: tt.channelRoles}
			got := p.RoleForChannel(tt.channelID)
			if got != tt.want {
				t.Errorf("RoleForChannel(%q) = %q, want %q", tt.channelID, got, tt.want)
			}
		})
	}
}

func TestParseSlackChannels(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{"empty string", "", nil},
		{"single channel", "C123", []string{"C123"}},
		{"multiple channels", "C123,C456,C789", []string{"C123", "C456", "C789"}},
		{"with spaces", "C123, C456 , C789", []string{"C123", "C456", "C789"}},
		{"trailing comma", "C123,", []string{"C123"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseSlackChannels(tt.raw)
			if len(got) != len(tt.want) {
				t.Fatalf("parseSlackChannels(%q) = %v, want %v", tt.raw, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("parseSlackChannels(%q)[%d] = %q, want %q", tt.raw, i, got[i], tt.want[i])
				}
			}
		})
	}
}
