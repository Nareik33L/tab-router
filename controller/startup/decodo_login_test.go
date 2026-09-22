package startup

import (
	"bufio"
	"errors"
	"strings"
	"testing"
)

func TestReadDecodoLoginUsesEnvironment(t *testing.T) {
	called := false
	user, pass, err := ReadDecodoLogin("account", "secret", true, bufio.NewReader(strings.NewReader("")), func() (string, error) {
		called = true
		return "", nil
	})
	if err != nil || user != "account" || pass != "secret" || called {
		t.Fatalf("env login = %q %q %v called=%v", user, pass, err, called)
	}
}

func TestReadDecodoLoginPrompts(t *testing.T) {
	user, pass, err := ReadDecodoLogin("", "", true, bufio.NewReader(strings.NewReader("myuser\n")), func() (string, error) {
		return "p@ss", nil
	})
	if err != nil || user != "myuser" || pass != "p@ss" {
		t.Fatalf("prompt login = %q %q %v", user, pass, err)
	}
}

func TestReadDecodoLoginRequiresBoth(t *testing.T) {
	_, _, err := ReadDecodoLogin("", "secret", false, nil, nil)
	if !errors.Is(err, ErrDecodoLogin) {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatal("password leaked into the error")
	}
	_, _, err = ReadDecodoLogin("", "", true, bufio.NewReader(strings.NewReader("\n")), func() (string, error) {
		return "", nil
	})
	if !errors.Is(err, ErrDecodoLogin) {
		t.Fatalf("empty prompt: %v", err)
	}
}
