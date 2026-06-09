package domain

import "testing"

func TestDirectionString(t *testing.T) {
	tests := []struct {
		name string
		dir  Direction
		want string
	}{
		{"forward", Forward, "F"},
		{"reverse", Reverse, "R"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.dir.String(); got != tt.want {
				t.Errorf("Direction(%d).String() = %q, want %q", tt.dir, got, tt.want)
			}
		})
	}
}
