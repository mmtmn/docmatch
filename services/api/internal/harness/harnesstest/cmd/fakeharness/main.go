// Command fakeharness serves the in-process fake of the harness on a Unix
// socket, replaying the public Vantara twin. Dev use, when the Rust
// harness is not built:
//
//	go run ./internal/harness/harnesstest/cmd/fakeharness -socket .cache/harness.sock
//
// Flags default to DM_HARNESS_SOCKET and the twin's expected.yaml found by
// walking up from the working directory.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mmtmn/docmatch/services/api/internal/harness/harnesstest"
)

func main() {
	socket := flag.String("socket", os.Getenv("DM_HARNESS_SOCKET"), "caminho do socket Unix (padrão: DM_HARNESS_SOCKET)")
	golden := flag.String("golden", "", "expected.yaml do gêmeo (padrão: procurado acima da pasta atual)")
	pause := flag.Int("pause-after", 0, "emite needs_approval depois de N achados (0 = nunca)")
	delay := flag.Duration("delay", 150*time.Millisecond, "intervalo entre eventos")
	flag.Parse()
	if err := run(*socket, *golden, *pause, *delay); err != nil {
		fmt.Fprintln(os.Stderr, "fakeharness:", err)
		os.Exit(1)
	}
}

func run(socket, golden string, pause int, delay time.Duration) error {
	if socket == "" {
		return fmt.Errorf("informe -socket ou DM_HARNESS_SOCKET")
	}
	if golden == "" {
		p, err := harnesstest.GoldenPath()
		if err != nil {
			return err
		}
		golden = p
	}
	script, err := harnesstest.LoadScript(golden)
	if err != nil {
		return err
	}
	srv := harnesstest.NewServer(script, harnesstest.Options{PauseAfterFindings: pause, Delay: delay})
	stop, err := harnesstest.Serve(socket, srv)
	if err != nil {
		return err
	}
	defer stop()
	fmt.Printf("fakeharness: caso %s em %s (%d achados, %d linhas de extrato)\n", script.Case, socket, len(script.Findings), len(script.BankLines))
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	fmt.Println("fakeharness: encerrando")
	return nil
}
