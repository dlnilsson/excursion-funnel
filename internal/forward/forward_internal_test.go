package forward

import (
	"errors"
	"testing"
)

func TestIsAuthError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{
			"attach rejected",
			errors.New("connect to Quack ledger quack:hub:9494: attach Quack ledger quack:hub:9494: Invalid Input Error: Authentication failed"),
			true,
		},
		{
			"art insert crash keeps credential",
			errors.New("insert remote requests: Invalid Input Error: Invalid node type for ARTOperator::Insert."),
			false,
		},
		{
			"generic transport error keeps credential",
			errors.New("insert remote requests: connection reset by peer"),
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isAuthError(tt.err); got != tt.want {
				t.Fatalf("isAuthError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
