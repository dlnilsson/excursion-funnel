// Package testutil provides shared helpers for repository tests.
package testutil

import (
	"bytes"
	"runtime"
	"runtime/pprof"
	"strings"
	"testing"
	"time"
)

// AssertNoGoroutineLeaks fails if the Go runtime reports a leaked goroutine
// whose stack contains one of fragments. Restricting the check to repository
// stacks avoids failures caused by unrelated runtime or dependency goroutines.
func AssertNoGoroutineLeaks(t testing.TB, fragments ...string) {
	t.Helper()
	profile := pprof.Lookup("goroutineleak")
	if profile == nil {
		t.Fatal("goroutineleak profile is unavailable")
	}

	var (
		profileText bytes.Buffer
		matches     []string
		deadline    = time.Now().Add(time.Second)
	)
	for {
		profileText.Reset()
		if err := profile.WriteTo(&profileText, 2); err != nil {
			t.Fatalf("write goroutineleak profile: %v", err)
		}
		text := profileText.String()
		matches = matches[:0]
		for _, fragment := range fragments {
			if strings.Contains(text, fragment) {
				matches = append(matches, fragment)
			}
		}
		if len(matches) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("leaked repository goroutines matching %q:\n%s", matches, text)
		}
		runtime.Gosched()
	}
}
