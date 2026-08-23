package pick_test

import (
	"net/http"
	"testing"

	"github.com/dlnilsson/excursion-funnel/internal/pick"
)

func TestFirstReturnsFirstNonZeroValue(t *testing.T) {
	if got := pick.First("", "", "third", "fourth"); got != "third" {
		t.Fatalf("First() = %q, want %q", got, "third")
	}
	if got := pick.First("", ""); got != "" {
		t.Fatalf("First() = %q, want empty", got)
	}
	if got := pick.First[int](); got != 0 {
		t.Fatalf("First() = %d, want 0", got)
	}
}

func TestFirstTreatsNilPointerAsAbsent(t *testing.T) {
	var (
		absent  *int64
		present = int64(7)
	)
	got := pick.First(absent, &present)
	if got == nil || *got != present {
		t.Fatalf("First() = %v, want pointer to %d", got, present)
	}
	if pick.First(absent, absent) != nil {
		t.Fatal("First() over all-nil pointers should be nil")
	}
}

func TestFirstFuncResolvesKeysInOrder(t *testing.T) {
	header := http.Header{}
	header.Set("session_id", "legacy")
	if got := pick.FirstFunc(header.Get, "session-id", "session_id"); got != "legacy" {
		t.Fatalf("FirstFunc() = %q, want %q", got, "legacy")
	}
	header.Set("session-id", "preferred")
	if got := pick.FirstFunc(header.Get, "session-id", "session_id"); got != "preferred" {
		t.Fatalf("FirstFunc() = %q, want %q", got, "preferred")
	}
	if got := pick.FirstFunc(header.Get, "absent"); got != "" {
		t.Fatalf("FirstFunc() = %q, want empty", got)
	}
}
