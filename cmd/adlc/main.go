// Command adlc is the control plane: the only writer to the ledger.
//
// Exit codes are part of the contract, and one of them exists because of a
// specific failure. A verifier that reports "I could not tell" as a failure
// gets ignored; a verifier that reports it as a pass is worse. So a ledger
// this binary is too old to interpret exits 5 — distinct from 3, which
// accuses. The same distinction runs through the gate: red is 6, could-not-run
// is 7, and neither one is 0.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/CyborgShadow/ADLC/internal/config"
	"github.com/CyborgShadow/ADLC/internal/lease"
	"github.com/CyborgShadow/ADLC/internal/ledger"
	"github.com/CyborgShadow/ADLC/internal/prompt"
)

// Exit codes.
const (
	exitOK            = 0
	exitUsage         = 1
	exitRefused       = 2
	exitTampered      = 3
	exitClauseMissing = 4
	exitLedgerUnknown = 5
	exitGateRed       = 6
	exitGateUnknown   = 7
)

type env struct {
	cfg     *config.Config
	led     *ledger.Ledger
	lib     *prompt.Library
	leases  *lease.Store
	actor   string
	repo    string
	jsonOut bool
}

var (
	flagDB     string
	flagConfig string
	flagActor  string
	flagRepo   string
	flagJSON   bool
)

func main() {
	fs := flag.NewFlagSet("adlc", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&flagDB, "db", filepath.Join(".adlc", "ledger.db"), "path to the ledger database")
	fs.StringVar(&flagConfig, "config", "adlc.json", "path to the project's declared config")
	fs.StringVar(&flagActor, "actor", "", "actor recorded on every row this invocation writes")
	fs.StringVar(&flagRepo, "repo", ".", "repository root")
	fs.BoolVar(&flagJSON, "json", false, "machine-readable output where a subcommand supports it")
	fs.Usage = usage

	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(exitUsage)
	}
	args := fs.Args()
	if len(args) == 0 {
		usage()
		os.Exit(exitUsage)
	}
	os.Exit(run(args))
}

func run(args []string) int {
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "help", "-h", "--help":
		usage()
		return exitOK
	case "init":
		return cmdInit(rest)
	case "segment":
		return withEnv(rest, cmdSegment)
	case "item":
		return withEnv(rest, cmdItem)
	case "run":
		return withEnv(rest, cmdRun)
	case "gate":
		return withEnv(rest, cmdGate)
	case "transition":
		return withEnv(rest, cmdTransition)
	case "lease":
		return withEnv(rest, cmdLease)
	case "question":
		return withEnv(rest, cmdQuestion)
	case "approval":
		return withEnv(rest, cmdApproval)
	case "report":
		return withEnv(rest, cmdReport)
	case "ledger":
		return withEnv(rest, cmdLedger)
	case "prompt":
		return withEnv(rest, cmdPrompt)
	case "dispatch":
		return withEnv(rest, cmdDispatch)
	case "serve":
		return withEnv(rest, cmdServe)
	case "schedule":
		return withEnv(rest, cmdSchedule)
	}
	fmt.Fprintf(os.Stderr, "adlc: unknown command %q\n\n", cmd)
	usage()
	return exitUsage
}

// withEnv opens the config and ledger for a subcommand that needs both.
func withEnv(args []string, f func(*env, []string) int) int {
	cfg, err := config.Load(flagConfig)
	if err != nil {
		fmt.Fprintln(os.Stderr, "adlc:", err)
		return exitUsage
	}
	led, err := ledger.Open(flagDB)
	if err != nil {
		fmt.Fprintln(os.Stderr, "adlc: open ledger:", err)
		return exitUsage
	}
	defer led.Close()

	e := &env{cfg: cfg, led: led, actor: flagActor, repo: flagRepo, jsonOut: flagJSON}
	if e.actor == "" {
		// An unattributable row on an append-only chain cannot be corrected, only
		// annotated. Refusing here is cheaper than annotating later.
		e.actor = "cli"
	}
	e.leases = lease.New(resolve(cfg.Lease.Dir), cfg.Lease.TTLMinutes, cfg.Lease.StripTokens)
	if cfg.Prompts.Dir != "" {
		if lib, err := prompt.Load(cfg.Prompts); err == nil {
			e.lib = lib
		}
	}
	return f(e, args)
}

func resolve(p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(flagRepo, p)
}

func fail(format string, a ...any) int {
	fmt.Fprintf(os.Stderr, "adlc: "+format+"\n", a...)
	return exitUsage
}

func need(sub string, ok bool, want string) bool {
	if ok {
		return true
	}
	fmt.Fprintf(os.Stderr, "adlc %s: %s\n", sub, want)
	return false
}

func nowUTC() time.Time { return time.Now().UTC() }

func usage() {
	fmt.Fprint(os.Stderr, `adlc — the control plane for an agentic delivery lifecycle.
It is the only writer to the ledger. Agents propose; this decides; both answers are recorded.

Usage: adlc [global flags] <command> [args]

  init                              create the ledger and seed the declared workers
  segment  create|list              register and inspect build segments
  item     create|list|show|amend   register and inspect work items
  run      start|finish|list|show   record a run's start and end facts by hand
  gate     run                      run the declared checks HERE and report what was observed
  transition propose|table          put a state change to the authority; print the authority table
  lease    acquire|release|list     the ephemeral claim store (items and resources)
  question raise|answer|list        the decisions an agent could not make for itself
  approval request|list|decide      the approval gate in front of anything irreversible
  report   fleet|segment            deterministic projections of the ledger
  ledger   verify|events|head       walk the chain; integrity and knowledge are answered separately
  prompt   list|show|check|assemble the versioned prompt library
  dispatch once|loop|plan           select work, invoke an agent, put the result to the authority
  schedule run|once|status         the scheduled lanes, and their derived liveness
  serve                             the operator dashboard on loopback

Global flags:
  -config string   declared project config (default "adlc.json")
  -db string       ledger database (default ".adlc/ledger.db")
  -actor string    recorded on every row this invocation writes
  -repo string     repository root (default ".")
  -json            machine-readable output where supported

Exit codes:
  0 ok
  1 usage or write error
  2 a transition was refused
  3 the ledger is TAMPERED — the record does not describe itself
  4 a prompt lost a mandatory safety clause
  5 the ledger is UNKNOWN to this build — upgrade the binary; the chain is fine
  6 the gate is RED
  7 the gate could not be run — which is not a pass
`)
}

func joinNonEmpty(parts []string, sep string) string {
	var out []string
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}
