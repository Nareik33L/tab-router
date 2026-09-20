package provider

import (
	"fmt"
	"os"
)

// resolvePassword returns the effective password for a route definition,
// reading password_env when set. The value must never be logged.
func resolvePassword(def RouteDef) (string, error) {
	if def.PasswordEnv != "" {
		v, ok := os.LookupEnv(def.PasswordEnv)
		if !ok {
			return "", fmt.Errorf("route %s: environment variable %s is not set", def.ID, def.PasswordEnv)
		}
		return v, nil
	}
	return def.Password, nil
}
