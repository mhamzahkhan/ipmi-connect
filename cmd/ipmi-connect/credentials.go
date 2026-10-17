package main

import "fmt"

const (
	envUser     = "IPMI_CONNECT_USER"
	envPassword = "IPMI_CONNECT_PASSWORD"
	defaultUser = "root"
)

// resolveCredentials applies precedence: command-line flag, then environment variable,
// then the default user / an interactive password prompt.
func resolveCredentials(opts options, getenv func(string) string, prompt func(string) (string, error)) (user, password string, err error) {
	user = firstNonEmpty(opts.User, getenv(envUser), defaultUser)
	password = firstNonEmpty(opts.Password, getenv(envPassword))
	if password == "" {
		password, err = prompt(fmt.Sprintf("Enter password for %s@%s: ", user, opts.Host))
	}
	return user, password, err
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
