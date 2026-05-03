package main

import (
	"fmt"
	"os"

	"golang.org/x/crypto/bcrypt"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "serve":
		if err := runServe(); err != nil {
			fmt.Fprintf(os.Stderr, "serve error: %v\n", err)
			os.Exit(1)
		}
	case "agent":
		if err := runAgent(); err != nil {
			fmt.Fprintf(os.Stderr, "agent error: %v\n", err)
			os.Exit(1)
		}
	case "hash-password":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: home-router hash-password <password>")
			os.Exit(1)
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(os.Args[2]), bcrypt.DefaultCost)
		if err != nil {
			fmt.Fprintf(os.Stderr, "hash error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(hash))
	case "gen-cert":
		if err := runGenCert(); err != nil {
			fmt.Fprintf(os.Stderr, "gen-cert error: %v\n", err)
			os.Exit(1)
		}
	case "render-configs":
		if err := runRenderConfigs(); err != nil {
			fmt.Fprintf(os.Stderr, "render-configs error: %v\n", err)
			os.Exit(1)
		}
	case "version":
		fmt.Printf("home-router %s (commit: %s, built: %s)\n", version, commit, date)
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `home-router — DIY home router management software

Usage:
  home-router <command> [options]

Commands:
  serve          Start web server (unprivileged)
  agent          Start privileged agent (root, UDS listener)
  hash-password  Print a bcrypt hash for the given password
  gen-cert       Generate the self-signed TLS cert/key and exit
  render-configs Render all service templates to /etc/* and exit (no reload)
  version        Show version info
  help           Show this help message
`)
}
