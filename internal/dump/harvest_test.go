package dump

import (
	"log/slog"
	"testing"
)

func TestPaged_stopsOnShortPage(t *testing.T) {
	calls := 0
	log := slog.Default()
	result := paged(log, "test", func(page int) ([]int, error) {
		calls++
		if page == 1 {
			// return a full page so loop continues
			out := make([]int, pageSize)
			for i := range out {
				out[i] = i
			}
			return out, nil
		}
		if page == 2 {
			// return a partial page (< pageSize) so loop stops
			return []int{1000, 2000}, nil
		}
		t.Fatalf("unexpected call at page %d", page)
		return nil, nil
	})
	if calls != 2 {
		t.Errorf("fetch called %d times, want 2", calls)
	}
	if len(result) != pageSize+2 {
		t.Errorf("len(result)=%d, want %d", len(result), pageSize+2)
	}
}

func TestPaged_stopsOnEmpty(t *testing.T) {
	calls := 0
	log := slog.Default()
	result := paged(log, "test", func(page int) ([]int, error) {
		calls++
		if page == 1 {
			out := make([]int, pageSize)
			return out, nil
		}
		return nil, nil // empty page
	})
	if calls != 2 {
		t.Errorf("calls=%d want 2", calls)
	}
	if len(result) != pageSize {
		t.Errorf("len=%d want %d", len(result), pageSize)
	}
}

func TestContainsFold(t *testing.T) {
	cases := []struct {
		s, sub string
		want   bool
	}{
		{"404 Not Found", "404", true},
		{"NOT FOUND", "not found", true},
		{"hello world", "goodbye", false},
	}
	for _, c := range cases {
		if got := containsFold(c.s, c.sub); got != c.want {
			t.Errorf("containsFold(%q,%q)=%v want %v", c.s, c.sub, got, c.want)
		}
	}
}

func TestLoginSourceTypeName(t *testing.T) {
	cases := map[int]string{
		2: "ldap", 3: "smtp", 6: "oauth2", 99: "unknown(99)",
	}
	for in, want := range cases {
		if got := loginSourceTypeName(in); got != want {
			t.Errorf("loginSourceTypeName(%d)=%q want %q", in, got, want)
		}
	}
}
