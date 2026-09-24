package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

// runners holds the subcommands that report failure as an error. Each
// one's error is printed as "<name> error: ..." and exits 1.
var runners = map[string]func() error{
	"serve":          runServe,
	"agent":          runAgent,
	"gen-cert":       runGenCert,
	"render-configs": runRenderConfigs,
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch name := os.Args[1]; name {
	case "hash-password":
		runHashPassword()
	case "version":
		fmt.Printf("lankeeper %s (commit: %s, built: %s)\n", version, commit, date)
	case "help", "-h", "--help":
		printUsage()
	default:
		runSubcommand(name)
	}
}

// runSubcommand dispatches to a runner and exits 1 on failure or on an
// unknown name.
func runSubcommand(name string) {
	run, ok := runners[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", name)
		printUsage()
		os.Exit(1)
	}
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "%s error: %v\n", name, err)
		os.Exit(1)
	}
}

// runHashPassword prints the bcrypt hash of the password read from stdin.
//
// Reading the password as a CLI argument would expose it in
// /proc/<pid>/cmdline and ps output for the duration of the bcrypt call,
// so it is required on stdin instead. A positional arg is refused so a
// stale caller fails loudly rather than silently leaking the value.
func runHashPassword() {
	if len(os.Args) > 2 {
		const usage = "lankeeper hash-password: password must be supplied on stdin, not as a CLI argument\n" +
			"  example: printf '%s' \"$password\" | lankeeper hash-password\n"
		_, _ = io.WriteString(os.Stderr, usage)
		os.Exit(2)
	}
	password, err := readPasswordFromStdin()
	if err != nil {
		fmt.Fprintf(os.Stderr, "hash-password: %v\n", err)
		os.Exit(1)
	}
	if password == "" {
		fmt.Fprintln(os.Stderr, "hash-password: empty password from stdin")
		os.Exit(1)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hash error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(hash))
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `lankeeper — DIY home router management software

Usage:
  lankeeper <command> [options]

Commands:
  serve          Start web server (unprivileged)
  agent          Start privileged agent (root, UDS listener)
  hash-password  Read a password from stdin and print its bcrypt hash
  gen-cert       Generate the self-signed TLS cert/key and exit
  render-configs Render all service templates to /etc/* and exit (no reload)
  version        Show version info
  help           Show this help message
`)
}

// readPasswordFromStdin returns the first line read from os.Stdin
// with the trailing CR/LF stripped. Returns an error when stdin is a
// terminal (no pipe) so callers fail loudly instead of waiting on a
// human-typed password — that interactive path would leak nothing
// but would also never finish in a deploy script.
func readPasswordFromStdin() (string, error) {
	info, err := os.Stdin.Stat()
	if err != nil {
		return "", fmt.Errorf("stat stdin: %w", err)
	}
	if (info.Mode() & os.ModeCharDevice) != 0 {
		// Use a literal so the %s in the example is never treated as
		// a format directive by govet/printf checks.
		return "", errors.New("stdin is a terminal; pipe the password instead, e.g. printf '%s' \"$password\" | lankeeper hash-password")
	}
	r := bufio.NewReader(os.Stdin)
	line, err := r.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("read stdin: %w", err)
	}
	return strings.TrimRight(line, "\r\n"), nil
}
