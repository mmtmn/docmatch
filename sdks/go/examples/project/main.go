// Apache-2.0. Inspect a saved project; only spend within an explicitly approved cap.
package main

import (
	"context"
	"fmt"
	dm "github.com/mmtmn/docmatch/sdks/go"
	"log"
	"os"
	"strconv"
	"time"
)

func main() {
	if len(os.Args) != 2 {
		log.Fatal("uso: go run ./examples/project prj_ID")
	}
	client, err := dm.New(os.Getenv("DM_API_BASE_URL"), os.Getenv("DOCMATCH_API_KEY"))
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	project := client.Project(os.Args[1])
	saved, err := project.Get(ctx)
	if err != nil {
		log.Fatal(err)
	}
	analysis, err := project.Estimate(ctx, saved.Revision, "")
	if err != nil {
		log.Fatal(err)
	}
	if analysis.Run != nil {
		fmt.Println("Conciliação existente:", analysis.Run.ID, analysis.Run.Status)
		return
	}
	if analysis.Quote == nil {
		log.Fatal("Resultado indisponível; confira o projeto no app.")
	}
	fmt.Printf("%s · limite: %d créditos\n", saved.Name, analysis.Quote.CapCredits)
	approved, _ := strconv.ParseInt(os.Getenv("DOCMATCH_APPROVE_CAP"), 10, 64)
	if approved < analysis.Quote.CapCredits || approved <= 0 {
		fmt.Println("Para aprovar, defina DOCMATCH_APPROVE_CAP com o limite aceito e execute novamente.")
		return
	}
	result, err := project.Reconcile(ctx, saved.Revision, analysis.Quote, "")
	if err != nil {
		log.Fatal(err)
	}
	if result.Run == nil {
		log.Fatal("Resposta sem conciliação")
	}
	run, err := project.Wait(ctx, result.Run.ID, nil)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("Concluída:", run.ID)
}
