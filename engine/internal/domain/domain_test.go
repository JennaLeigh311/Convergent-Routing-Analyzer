package domain

import "testing"

func TestDirectionString(test1 *testing.T) {
	tests := []struct {
		name string
		dir  Direction
		want string
	}{
		{"forward", Forward, "F"},
		{"reverse", Reverse, "R"},
		{"out of range surfaces, not silently forward", Direction(7), "Direction(7)"},
	}
	for _, testCase := range tests {
		test1.Run(testCase.name, func(test2 *testing.T) {
			if got := testCase.dir.String(); got != testCase.want {
				test2.Errorf("Direction(%d).String() = %q, want %q", testCase.dir, got, testCase.want)
			}
		})
	}
}
