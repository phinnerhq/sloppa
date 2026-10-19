package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

type credentials struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

var errNotSignedIn = userErrorf("Not signed in to sloppa: run `sloppa setup '<setup-link>'` with a link from the dashboard, set SLOPPA_URL and SLOPPA_TOKEN, or pipe a machine token in:\n  %s", signInAgain)

// credentialsPath is where `sloppa login` keeps the server and token, readable by this user alone:
// the user's config directory, as the OS has it.
func credentialsPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "sloppa", "credentials.json"), nil
}

func readCredentials() (credentials, bool) {
	path, err := credentialsPath()
	if err != nil {
		return credentials{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return credentials{}, false
	}
	var c credentials
	if json.Unmarshal(data, &c) != nil {
		return credentials{}, false
	}
	return c, true
}

// loadCredentials lets SLOPPA_URL and SLOPPA_TOKEN win over the file, each on its own, for a
// script with a token of its own, or another server.
func loadCredentials() (credentials, error) {
	c := credentials{URL: os.Getenv("SLOPPA_URL"), Token: os.Getenv("SLOPPA_TOKEN")}
	if c.URL == "" || c.Token == "" {
		stored, ok := readCredentials()
		if !ok {
			return credentials{}, errNotSignedIn
		}
		if c.URL == "" {
			c.URL = stored.URL
		}
		if c.Token == "" {
			c.Token = stored.Token
		}
	}
	c.URL = strings.TrimRight(c.URL, "/")
	return c, nil
}

// saveCredentials writes them whole or not at all, readable by this user alone, and says where.
func saveCredentials(c credentials) (string, error) {
	path, err := credentialsPath()
	if err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".credentials-*.json")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(append(data, '\n'))
	err = errors.Join(err, tmp.Sync(), tmp.Close(), os.Chmod(tmp.Name(), 0o600))
	if err != nil {
		return "", err
	}
	return path, os.Rename(tmp.Name(), path)
}
