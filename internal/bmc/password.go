package bmc

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ReadPassword prompts the user and reads a password with terminal echo disabled.
func ReadPassword(prompt string) (string, error) {
	fmt.Print(prompt)
	defer fmt.Println()

	// Disable terminal echo using stty
	cmd := exec.Command("stty", "-echo")
	cmd.Stdin = os.Stdin
	_ = cmd.Run()

	defer func() {
		// Restore terminal echo
		restoreCmd := exec.Command("stty", "echo")
		restoreCmd.Stdin = os.Stdin
		_ = restoreCmd.Run()
	}()

	reader := bufio.NewReader(os.Stdin)
	pass, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}

	return strings.TrimRight(pass, "\r\n"), nil
}
