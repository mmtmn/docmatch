package config

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

const samplePoolSecret = "segredo-do-pool-com-mais-de-32-caracteres"

func TestBurstDefaultsKeepEverythingLocal(t *testing.T) {
	t.Parallel()
	cfg := mustLoad(t, validEnv())
	b, d := cfg.Burst, cfg.Deadline
	if b.Enabled || b.Listen != "" || b.ASGName != "" || b.MemoryHeadroomMB != DefaultBurstMemoryHeadroomMB || !slices.Equal(b.Kinds, []string{"audio", "video"}) ||
		b.MaxInstances != 2 || b.MonthlyHours != 60 || b.IdleAfter != 10*time.Minute || b.ClaimTimeout != 10*time.Minute || b.MinSeconds != 0 {
		t.Fatalf("burst = %+v", b)
	}
	if d.Min != 2*time.Hour || d.Max != 24*time.Hour || d.Factor != 3 {
		t.Fatalf("deadline = %+v", d)
	}
}

func TestBurstSettings(t *testing.T) {
	t.Parallel()
	env := validEnv()
	env[VarBurstEnabled] = "1"
	env[VarBurstListen] = "0.0.0.0:8444"
	env[VarBurstPoolSecret] = samplePoolSecret
	env[VarBurstASGName] = "docmatch-prod-worker"
	env[VarBurstMinSeconds] = "900"
	env[VarBurstMemoryHeadroom] = "256"
	env[VarBurstKinds] = "audio,video,document"
	env[VarBurstMaxInstances] = "4"
	env[VarBurstMonthlyHours] = "12.5"
	env[VarBurstIdleMinutes] = "5"
	env[VarBurstClaimSeconds] = "300"
	env[VarRunDeadlineMin] = "90m"
	env[VarRunDeadlineMax] = "12h"
	env[VarRunDeadlineFactor] = "2.5"
	cfg := mustLoad(t, env)
	b, d := cfg.Burst, cfg.Deadline
	if !b.Enabled || b.Listen != "0.0.0.0:8444" || b.PoolSecret.Reveal() != samplePoolSecret || b.ASGName != "docmatch-prod-worker" || b.MinSeconds != 900 ||
		b.MemoryHeadroomMB != 256 || len(b.Kinds) != 3 || b.MaxInstances != 4 || b.MonthlyHours != 12.5 || b.IdleAfter != 5*time.Minute || b.ClaimTimeout != 5*time.Minute {
		t.Fatalf("burst = %+v", b)
	}
	if d.Min != 90*time.Minute || d.Max != 12*time.Hour || d.Factor != 2.5 {
		t.Fatalf("deadline = %+v", d)
	}
	// The pool secret never leaks; the switches show.
	out := cfg.String() + fmt.Sprintf("%+v %#v", *cfg, *cfg)
	if strings.Contains(out, samplePoolSecret) || !strings.Contains(cfg.String(), VarBurstPoolSecret+"="+Redacted) || !strings.Contains(cfg.String(), VarBurstEnabled+"=true") {
		t.Fatalf("redação: %s", cfg.String())
	}
	// An empty list switches the kinds off.
	env[VarBurstKinds] = ""
	if k := mustLoad(t, env).Burst.Kinds; len(k) != 2 {
		t.Fatalf("vazio volta ao padrão: %v", k)
	}
}

func TestBurstSettingsProblems(t *testing.T) {
	t.Parallel()
	cases := map[string]map[string]string{
		VarBurstPoolSecret:     {VarBurstEnabled: "1"},
		VarBurstListen:         {VarBurstListen: "endereço", VarBurstPoolSecret: samplePoolSecret},
		VarBurstMaxInstances:   {VarBurstMaxInstances: "-1"},
		VarBurstMonthlyHours:   {VarBurstMonthlyHours: "muitas"},
		VarBurstIdleMinutes:    {VarBurstIdleMinutes: "0"},
		VarBurstClaimSeconds:   {VarBurstClaimSeconds: "5"},
		VarBurstMemoryHeadroom: {VarBurstMemoryHeadroom: "x"},
		VarRunDeadlineMin:      {VarRunDeadlineMin: "duas horas"},
		VarRunDeadlineMax:      {VarRunDeadlineMin: "3h", VarRunDeadlineMax: "1h"},
		VarRunDeadlineFactor:   {VarRunDeadlineFactor: "0.5"},
		VarBurstKinds:          {VarBurstKinds: "Áudio"},
	}
	for want, extra := range cases {
		env := validEnv()
		for k, v := range extra {
			env[k] = v
		}
		_, err := LoadFrom(lookupFrom(env))
		if err == nil || !slices.Contains(validationVars(t, err), want) {
			t.Errorf("%s: err = %v", want, err)
		}
	}
	env := validEnv()
	env[VarBurstListen] = "0.0.0.0:8444"
	env[VarBurstPoolSecret] = "curto"
	if _, err := LoadFrom(lookupFrom(env)); err == nil || !slices.Contains(validationVars(t, err), VarBurstPoolSecret) {
		t.Fatalf("segredo curto: %v", err)
	}
}

func TestLoadBurstWorker(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		VarEnv: "prod", VarBurstAPIURL: "https://10.40.0.5:8444", VarBurstPoolSecret: samplePoolSecret,
		VarHarnessSocket: "/run/docmatch-harness/harness.sock", VarMediaLocalDir: "/var/lib/docmatch/media",
		// The worker never needs (nor reads) the database.
		VarDatabaseURL: "postgres://nao-usado",
	}
	w, err := LoadBurstWorkerFrom(lookupFrom(env))
	if err != nil {
		t.Fatal(err)
	}
	if w.Env != EnvProd || w.APIURL.Host != "10.40.0.5:8444" || w.HarnessSocket == "" || w.MediaLocalDir != "/var/lib/docmatch/media" {
		t.Fatalf("worker = %+v", w)
	}
	for name, change := range map[string]map[string]string{
		VarBurstAPIURL:     {VarBurstAPIURL: "http://10.40.0.5:8444"},
		VarBurstPoolSecret: {VarBurstPoolSecret: "curto"},
		VarHarnessSocket:   {VarHarnessSocket: ""},
	} {
		e := map[string]string{}
		for k, v := range env {
			e[k] = v
		}
		for k, v := range change {
			e[k] = v
		}
		if _, err := LoadBurstWorkerFrom(lookupFrom(e)); err == nil || !slices.Contains(validationVars(t, err), name) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	e := map[string]string{VarBurstAPIURL: "::nada", VarBurstPoolSecret: samplePoolSecret, VarHarnessSocket: "s"}
	if _, err := LoadBurstWorkerFrom(lookupFrom(e)); err == nil {
		t.Fatal("URL inválida")
	}
	for _, v := range BurstWorkerVars() {
		if !slices.Contains(KnownVars(), v) {
			t.Errorf("%s fora de KnownVars", v)
		}
	}
}

func TestLoadBurstWorkerReadsTheProcessEnv(t *testing.T) {
	t.Setenv(VarBurstAPIURL, "https://10.40.0.5:8444")
	t.Setenv(VarBurstPoolSecret, samplePoolSecret)
	t.Setenv(VarHarnessSocket, "/run/h.sock")
	if w, err := LoadBurstWorker(); err != nil || w.APIURL.Port() != "8444" {
		t.Fatalf("worker = %+v %v", w, err)
	}
}
