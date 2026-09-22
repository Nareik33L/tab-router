package startup

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ErrDecodoLogin means startup cannot continue because the Decodo proxy
// username or password was not provided. The error text never contains
// the password.
var ErrDecodoLogin = errors.New("Decodo username and password are required")

// ReadDecodoLogin returns the proxy username and password for this start.
// Environment values are used when both are set. Otherwise an interactive
// start asks for them. The password reader is responsible for not echoing
// the password. The returned password is never included in an error.
func ReadDecodoLogin(envUser, envPass string, interactive bool, in *bufio.Reader, readPassword func() (string, error)) (string, string, error) {
	envUser = strings.TrimSpace(envUser)
	if envUser != "" && envPass != "" {
		return envUser, envPass, nil
	}
	if !interactive || in == nil || readPassword == nil {
		return "", "", ErrDecodoLogin
	}
	fmt.Println("Decodo sign-in")
	fmt.Print("Username: ")
	user, err := in.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", "", err
	}
	user = strings.TrimSpace(user)
	fmt.Print("Password: ")
	pass, err := readPassword()
	fmt.Println()
	if err != nil {
		return "", "", err
	}
	pass = strings.TrimRight(pass, "\r\n")
	if user == "" || pass == "" {
		return "", "", ErrDecodoLogin
	}
	return user, pass, nil
}
