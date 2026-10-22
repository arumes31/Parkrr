package backup

import "testing"

func TestCheckPgRestoreVersion(t *testing.T) {
	cases := []struct {
		out    string
		wantOK bool
	}{
		{"pg_restore (PostgreSQL) 16.10\n", true},
		{"pg_restore (PostgreSQL) 16.13 (Ubuntu 16.13-1.pgdg24.04+1)\n", true},
		{"pg_restore (PostgreSQL) 16.9\n", false},
		{"pg_restore (PostgreSQL) 17.5\n", false},
		{"pg_restore (PostgreSQL) 17.6\n", true},
		{"pg_restore (PostgreSQL) 18.0\n", true},
		{"pg_restore (PostgreSQL) 19beta1\n", true},
		{"pg_restore (PostgreSQL) 12.22\n", false},
		{"garbage", false},
	}
	for _, c := range cases {
		if err := checkPgRestoreVersion(c.out); (err == nil) != c.wantOK {
			t.Errorf("%q: err = %v, wantOK %v", c.out, err, c.wantOK)
		}
	}
}
