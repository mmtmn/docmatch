package version

import (
	"strings"
	"testing"
)

func TestCurrentCarriesBuildMetadata(t *testing.T) {
	t.Parallel()
	info := Current()
	if info.Version != Version || info.GitSHA != GitSHA || info.BuiltAt != BuiltAt {
		t.Fatalf("Current() = %+v, esperado os valores do pacote", info)
	}
	if !strings.HasPrefix(info.Go, "go") {
		t.Fatalf("Go = %q, esperado prefixo go", info.Go)
	}
}
