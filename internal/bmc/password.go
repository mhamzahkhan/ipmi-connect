package bmc

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"golang.org/x/term"
)

// exitInterrupted is the conventional exit status after SIGINT (128 + 2).
const exitInterrupted = 130

// ReadPassword prompts the user and reads a password with terminal echo disabled. Input that is
// not a terminal (a pipe or file) is read as one line.
func ReadPassword(prompt string) (string, error) {
	fmt.Print(prompt)
	defer fmt.Println()

	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return readLine(os.Stdin)
	}

	// term.ReadPassword leaves echo off if the process is killed mid-read, so restore the
	// terminal ourselves on Ctrl+C before exiting.
	state, err := term.GetState(fd)
	if err != nil {
		return "", err
	}
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigs)
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-sigs:
			_ = term.Restore(fd, state)
			fmt.Println()
			os.Exit(exitInterrupted)
		case <-done:
		}
	}()

	pass, err := term.ReadPassword(fd)
	return string(pass), err
}

// readLine reads one line, without its line ending. A final line without a newline counts.
func readLine(r io.Reader) (string, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if errors.Is(err, io.EOF) && line != "" {
		err = nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
