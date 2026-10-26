// Command oddsfeed is the Go port of the DraftKings and FanDuel research
// probes. Each subcommand mirrors one Node script:
//
//	dk-snapshot  dk-http.cjs
//	dk-ws        probe-ws.cjs dk / compare-dk.cjs
//	fd-discover  fetch.cjs + extract-fd-nfl.cjs
//	fd-rest      probe-fd-rest.cjs
//	fd-ws        probe-ws.cjs fd
//	verify       all REST and WebSocket paths in one run
//
// Output defaults to ./out because the pre-existing ./captures directory is
// read-only in this session; pass -log/-out to redirect.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "dk-snapshot":
		err = cmdDKsnapshot(args)
	case "dk-discover":
		err = cmdDKdiscover(args)
	case "dk-ws":
		err = cmdDKws(args)
	case "fd-discover":
		err = cmdFDdiscover(args)
	case "fd-rest":
		err = cmdFDrest(args)
	case "fd-ws":
		err = cmdFDws(args)
	case "fd-event":
		err = cmdFDevent(args)
	case "fd-events":
		err = cmdFDevents(args)
	case "pm-discover":
		err = cmdPMdiscover(args)
	case "pm-ws":
		err = cmdPMws(args)
	case "verify":
		err = cmdVerify(args)
	case "soak":
		err = cmdSoak(args)
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `oddsfeed - DraftKings / FanDuel Ontario REST and WebSocket client (Go)

Usage:
  oddsfeed dk-snapshot  [-url URL] [-out FILE] [-timeout DUR]
  oddsfeed dk-discover  [-sport nba|nfl] [-out DIR] [-no-props]
                        [-props sub1,sub2] [-timeout DUR]
  oddsfeed dk-ws        [-mode probe|partials] [-seconds N] [-log FILE] [-snapshot-url URL]
  oddsfeed fd-discover  [-ak KEY] [-out FILE] [-normalized FILE]
  oddsfeed fd-rest      [-nfl FILE] [-ids id,id] [-mode both|graphql|smp] [-limit N]
  oddsfeed fd-ws        [-nfl FILE | -live | -ids id,id] [-seconds N] [-log FILE]
  oddsfeed fd-event     -event ID [-mode popular|mga|both] [-tab popular]
                        [-prices] [-out DIR]
  oddsfeed fd-events    [-sport nba|nfl] [-max-events N] [-prices] [-out DIR]
  oddsfeed fd-events    [-sport nba|nfl] [-max-events N] [-prices] [-out DIR]
  oddsfeed pm-discover  [-sport nba|nfl] [-series ID] [-event ID] [-limit N] [-out FILE]
  oddsfeed pm-ws        [-event ID | -sport nba|nfl | -ids id,id]
                        [-types main,alt,props,all] [-seconds N] [-log FILE]
  oddsfeed verify       [-seconds N] [-skip-ws]
  oddsfeed soak         [-duration 1h] [-out DIR] [-dk-rest-every 30s]
                        [-fd-poll-every 30s] [-fd-discovery-every 5m]
                        [-dk-scope partials|probe] [-fd-markets N]
                        [-ping-every 30s] [-stale-after 10m]
                        [-reconnect-every 0] [-verbose]

Environment:
  DK_USER_AGENT  override the DraftKings User-Agent (default: tested Chrome 156)
  CAPTURE_MS     default WebSocket observation window in milliseconds (45000)
`)
}

// envInt returns an integer environment override or def.
func envInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// splitIDs parses a comma-separated id list, ignoring blanks.
func splitIDs(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			if part := trimSpace(s[start:i]); part != "" {
				out = append(out, part)
			}
			start = i + 1
		}
	}
	return out
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t' || s[start] == '\n' || s[start] == '\r') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\n' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}
