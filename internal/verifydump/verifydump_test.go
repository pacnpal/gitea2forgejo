package verifydump

import "testing"

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{
		512:                    "512 B",
		2048:                   "2.0 KiB",
		2 * 1024 * 1024:        "2.0 MiB",
		3 * 1024 * 1024 * 1024: "3.0 GiB",
	}
	for n, want := range cases {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d)=%q want %q", n, got, want)
		}
	}
}

func TestAbs(t *testing.T) {
	if abs(-5) != 5 || abs(5) != 5 || abs(0) != 0 {
		t.Error("abs broken")
	}
}
