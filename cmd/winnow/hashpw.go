package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"
)

// bcryptCost is deliberately above the library default for a login that guards
// mail-mutating actions.
const bcryptCost = 12

// runHashpw prompts for a password (without echo when attached to a TTY) and
// prints a bcrypt hash suitable for APP_PASSWORD_HASH.
func runHashpw() error {
	pw, err := readPassword()
	if err != nil {
		return err
	}
	if len(pw) < 8 {
		return errors.New("password must be at least 8 characters")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcryptCost)
	if err != nil {
		return err
	}
	fmt.Println(string(hash))
	return nil
}

func readPassword() (string, error) {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		fmt.Fprint(os.Stderr, "Password: ")
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	// Non-interactive (piped) input: read a single line.
	sc := bufio.NewScanner(os.Stdin)
	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return "", err
		}
		return "", errors.New("no password provided on stdin")
	}
	return strings.TrimRight(sc.Text(), "\r\n"), nil
}
