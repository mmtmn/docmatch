package docker

import (
	"bufio"
	"errors"
	"os"
	"strings"
	"testing"
)

func lines(t *testing.T, name string) []string {
	t.Helper()
	f, err := os.Open("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out
}

func TestPullTracker(t *testing.T) {
	tr := NewPullTracker()
	var mid PullProgress
	for i, l := range lines(t, "pull.jsonl") {
		if _, ok := tr.Feed(l); !ok {
			t.Fatalf("linha %d não reconhecida: %s", i, l)
		}
		if i == 5 {
			mid = tr.Snapshot()
		}
	}
	if len(mid.Services) != 1 || mid.Services[0].Current != 40188 || mid.Services[0].Total != 80375 {
		t.Fatalf("meio do download: %+v", mid)
	}
	if f := mid.Services[0].Fraction(); f < 0.49 || f > 0.51 {
		t.Fatalf("fração %f", f)
	}
	end := tr.Snapshot().Services[0]
	if !end.Done || end.Failed || end.Fraction() != 1 {
		t.Fatalf("fim: %+v", end)
	}
	if _, ok := tr.Feed("Pulling postgres ... done"); ok {
		t.Fatal("texto puro não é evento")
	}
}

func TestPullDenied(t *testing.T) {
	tr := NewPullTracker()
	tail := make([]string, 0, 8)
	for _, l := range lines(t, "pull-denied.jsonl") {
		tr.Feed(l)
		tail = append(tail, l)
	}
	s := tr.Snapshot().Services
	if len(s) != 2 || !s[0].Failed || !s[1].Failed {
		t.Fatalf("%+v", s)
	}
	err := &Error{Msg: lastMeaningful(tail, errors.New("exit status 1"))}
	if !strings.Contains(err.Error(), "denied") || !IsNotFound(err) {
		t.Fatalf("erro: %v", err)
	}
}

func TestBuildTracker(t *testing.T) {
	tr := NewBuildTracker()
	var logs []string
	sawRunning := false
	for _, l := range lines(t, "build.jsonl") {
		got, _, ok := tr.Feed(l)
		if !ok {
			t.Fatalf("linha não reconhecida: %s", l)
		}
		logs = append(logs, got...)
		if s := tr.Snapshot().Services; len(s) == 1 && s[0].Current != "" {
			sawRunning = true
		}
	}
	s := tr.Snapshot().Services
	if len(s) != 1 || s[0].Name != "t" {
		t.Fatalf("%+v", s)
	}
	// Two stages: FROM + 2 RUN, then FROM + COPY + RUN; BuildKit merges the two identical FROM.
	if s[0].Total != 5 || s[0].Done != 5 || s[0].Failed || s[0].Fraction() != 1 {
		t.Fatalf("passos: %+v", s[0])
	}
	if !sawRunning {
		t.Fatal("nenhum passo em andamento visto")
	}
	joined := strings.Join(logs, "|")
	for _, want := range []string{"um", "dois", "tres", "quatro", "cinco"} {
		if !strings.Contains(joined, want) {
			t.Errorf("log sem %q: %s", want, joined)
		}
	}
}

func TestBuildTrackerBytesAndErrors(t *testing.T) {
	tr := NewBuildTracker()
	tr.Feed(`{"vertexes":[{"digest":"d1","name":"[api build 1/3] FROM docker.io/library/golang:1.27","started":"x"}]}`)
	tr.Feed(`{"statuses":[{"id":"sha256:a","vertex":"d1","current":50,"total":200},{"id":"sha256:b","vertex":"d1","current":0,"total":100}]}`)
	s := tr.Snapshot().Services[0]
	if s.CurrentBytes != 50 || s.TotalBytes != 300 || s.Done != 0 || s.Total != 1 {
		t.Fatalf("%+v", s)
	}
	tr.Feed(`{"vertexes":[{"digest":"d2","name":"[api build 2/3] RUN go build","started":"x","completed":"y","error":"exit code: 1"}]}`)
	if s := tr.Snapshot().Services[0]; !s.Failed {
		t.Fatalf("falha não vista: %+v", s)
	}
	if logs, _, ok := tr.Feed(`{"level":"warning","msg":"Docker Compose is configured to build using Bake"}`); !ok || len(logs) != 1 {
		t.Fatal("aviso do compose vira log")
	}
	if _, _, ok := tr.Feed("#5 DONE 0.1s"); ok {
		t.Fatal("texto puro não é evento")
	}
}

func TestParsePS(t *testing.T) {
	ndjson := `{"Service":"api","State":"running","Health":"healthy","Publishers":[{"URL":"127.0.0.1","TargetPort":8080,"PublishedPort":18080}]}
{"Service":"migrate","State":"exited","ExitCode":0,"Publishers":[]}
{"Service":"web","State":"running","Publishers":[{"URL":"127.0.0.1","TargetPort":4321,"PublishedPort":14321},{"URL":"::1","TargetPort":4321,"PublishedPort":14321}]}`
	list, err := ParsePS(ndjson)
	if err != nil || len(list) != 3 {
		t.Fatalf("%v %v", list, err)
	}
	p := PublishedPorts(list)
	if !p[18080] || !p[14321] || len(p) != 2 {
		t.Fatalf("%v", p)
	}
	arr, err := ParsePS(`[{"Service":"api","State":"running"}]`)
	if err != nil || len(arr) != 1 {
		t.Fatalf("formato antigo: %v %v", arr, err)
	}
	if empty, err := ParsePS("  \n"); err != nil || empty != nil {
		t.Fatal("vazio")
	}
	if _, err := ParsePS("{"); err == nil {
		t.Fatal("quebrado")
	}
}

func TestVersionsAndErrors(t *testing.T) {
	for _, c := range []struct {
		v, min string
		ok     bool
	}{
		{"2.40.3", "2.20", true}, {"v2.20.0", "2.20", true}, {"2.19.9", "2.20", false},
		{"2.40.3+ds1-0ubuntu1~24.04.1", "2.20", true}, {"1.29.2", "2.20", false}, {"", "2.20", false}, {"10", "2.20", true},
	} {
		if AtLeast(c.v, c.min) != c.ok {
			t.Errorf("AtLeast(%q, %q) != %v", c.v, c.min, c.ok)
		}
	}
	if ClassifyDaemonError("permission denied while trying to connect to the Docker daemon socket") != DaemonPermission {
		t.Error("permissão")
	}
	if ClassifyDaemonError("Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?") != DaemonDown {
		t.Error("parado")
	}
	if IsNotFound(errors.New("network timeout")) || IsNotFound(nil) || !IsNotFound(errors.New("manifest unknown")) {
		t.Error("IsNotFound")
	}
	if got := lastMeaningful([]string{"plain failure", `{"level":"info","msg":"x"}`}, errors.New("e")); got != "plain failure" {
		t.Errorf("lastMeaningful = %q", got)
	}
}
