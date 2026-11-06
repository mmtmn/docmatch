package workspace

import (
	"reflect"
	"testing"
)

func TestDropPathsAreData(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{`'/tmp/one file.csv' /tmp/two\ files.txt`, []string{"/tmp/one file.csv", "/tmp/two files.txt"}},
		{"file:///tmp/one%20file.csv\n/tmp/two.csv", []string{"/tmp/one file.csv", "/tmp/two.csv"}},
		{`"C:\Users\Demo\one file.csv" C:\data\two.csv`, []string{`C:\Users\Demo\one file.csv`, `C:\data\two.csv`}},
		{`'$(touch /tmp/not-executed).csv'`, []string{"$(touch /tmp/not-executed).csv"}},
	} {
		got, err := ParsePaths(tc.in)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%q: %v %v", tc.in, got, err)
		}
	}
	if _, err := ParsePaths(`'unterminated`); err == nil {
		t.Fatal("unclosed quote accepted")
	}
	if Safe("\x1b[31mname\u202e") != "[31mname" {
		t.Fatal("terminal control survived")
	}
}
