package tools

import "testing"

func TestVerboseFlagIsPersistent(t *testing.T) {
	cmd := New()
	if flag := cmd.PersistentFlags().Lookup("verbose"); flag == nil {
		t.Fatal("--verbose flag is not registered")
	}
}
