package main

import (
	"context"
	"encoding/json"
	"fmt"
	dm "github.com/mmtmn/docmatch/sdks/go"
	"os"
	"path/filepath"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	c, err := dm.New(os.Getenv("DM_API_BASE_URL"), os.Getenv("DOCMATCH_API_KEY"))
	if err != nil {
		return err
	}
	var ids []string
	for _, name := range os.Args[1:] {
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		mime := "text/csv"
		if filepath.Ext(name) == ".txt" {
			mime = "text/plain"
		}
		u, err := c.Upload(ctx, filepath.Base(name), mime, data)
		if err != nil {
			return err
		}
		ids = append(ids, u.ID)
	}
	input := c.Input(ids...)
	q, err := c.Estimate(ctx, input)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Approved example run: cap %d tokens\n", q.CapCredits)
	r, err := c.Run(ctx, q, input)
	if err != nil {
		return err
	}
	r, err = c.Wait(ctx, r.ID, nil)
	if err != nil {
		return err
	}
	f, err := c.Findings(ctx, r.ID)
	if err != nil {
		return err
	}
	data, err := c.Export(ctx, r.ID, "xlsx")
	if err != nil {
		return err
	}
	if path := os.Getenv("DOCMATCH_EXPORT_PATH"); path != "" {
		if err = os.WriteFile(path, data, 0o600); err != nil {
			return err
		}
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"run": r, "findings": f})
}
