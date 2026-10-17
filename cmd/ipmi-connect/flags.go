package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// options holds the parsed command line.
type options struct {
	Host      string
	User      string // empty means IPMI_CONNECT_USER, else root
	Password  string // empty means IPMI_CONNECT_PASSWORD, else prompt
	Type      string // auto, dell or supermicro
	WebPort   int
	NoBrowser bool
	ShowHelp  bool
}

const defaultWebPort = 8080

func printUsage() {
	fmt.Fprintf(os.Stderr, "Usage: %s <host> [options]\n\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "Modern, sleek replacement client for legacy Dell & Supermicro IPMI BMC consoles.\n\n")
	fmt.Fprintf(os.Stderr, "Options:\n")
	fmt.Fprintf(os.Stderr, "  -u, --user <username>     BMC username (default: $%s, else root)\n", envUser)
	fmt.Fprintf(os.Stderr, "  -p, --pass <password>     BMC password (default: $%s, else prompted).\n", envPassword)
	fmt.Fprintf(os.Stderr, "                            Prefer the env var: flags are visible in the process list.\n")
	fmt.Fprintf(os.Stderr, "  --type <auto|dell|supermicro>  Force BMC vendor type (default: auto)\n")
	fmt.Fprintf(os.Stderr, "  --web-port <port>         Local console UI port (default: 8080)\n")
	fmt.Fprintf(os.Stderr, "  --no-browser              Do not auto-open the web browser\n")
	fmt.Fprintf(os.Stderr, "  -h, --help                Show this help message\n\n")
	fmt.Fprintf(os.Stderr, "Examples:\n")
	fmt.Fprintf(os.Stderr, "  %s 192.0.2.10                       # prompts for the root password\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s 192.0.2.10 -u admin --no-browser\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s=... %s 192.0.2.20 --type supermicro\n", envPassword, os.Args[0])
}

// parseArgs accepts single- and double-dash flags, with values either as the next
// argument or after '='. A flag value that starts with '-' is treated as absent.
func parseArgs(args []string) options {
	o := options{Type: "auto", WebPort: defaultWebPort}

	// next returns the following argument as a value, unless it looks like a flag.
	next := func(i *int) (string, bool) {
		if *i+1 < len(args) && !strings.HasPrefix(args[*i+1], "-") {
			*i++
			return args[*i], true
		}
		return "", false
	}
	setPort := func(v string) {
		if p, err := strconv.Atoi(v); err == nil {
			o.WebPort = p
		}
	}

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-h" || arg == "--help" || arg == "-help":
			o.ShowHelp = true
		case arg == "--no-browser" || arg == "-no-browser":
			o.NoBrowser = true
		case arg == "-u" || arg == "--user" || arg == "-user":
			if v, ok := next(&i); ok {
				o.User = v
			}
		case strings.HasPrefix(arg, "-u="):
			o.User = strings.TrimPrefix(arg, "-u=")
		case strings.HasPrefix(arg, "--user="):
			o.User = strings.TrimPrefix(arg, "--user=")
		case arg == "-p" || arg == "--pass" || arg == "-pass":
			if v, ok := next(&i); ok {
				o.Password = v
			}
		case strings.HasPrefix(arg, "-p="):
			o.Password = strings.TrimPrefix(arg, "-p=")
		case strings.HasPrefix(arg, "--pass="):
			o.Password = strings.TrimPrefix(arg, "--pass=")
		case arg == "--type" || arg == "-type":
			if v, ok := next(&i); ok {
				o.Type = v
			}
		case strings.HasPrefix(arg, "--type="):
			o.Type = strings.TrimPrefix(arg, "--type=")
		case arg == "--web-port" || arg == "-web-port":
			if v, ok := next(&i); ok {
				setPort(v)
			}
		case strings.HasPrefix(arg, "--web-port="):
			setPort(strings.TrimPrefix(arg, "--web-port="))
		case arg == "--port" || arg == "-port":
			// Accepted for compatibility but unused: BMC ports come from the protocol.
			next(&i)
		case strings.HasPrefix(arg, "--port="):
			// Accepted for compatibility but unused.
		case !strings.HasPrefix(arg, "-"):
			if o.Host == "" {
				o.Host = arg
			}
		}
	}
	return o
}
