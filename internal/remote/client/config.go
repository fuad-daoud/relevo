package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"

	"github.com/fuad-daoud/relevo/internal/remote"
)

var (
	ErrNoKey     = errors.New("no client key; run relevo config server key")
	ErrKeyExists = errors.New("client key already exists")
)

func EncodeServers(s remote.Servers) ([]byte, error) {
	if s == nil {
		s = make(remote.Servers)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal servers: %w", err)
	}
	return append(data, '\n'), nil
}

func PublicComment() string {
	u, err := user.Current()
	if err != nil || u.Username == "" {
		return ""
	}
	if h, err := os.Hostname(); err == nil && h != "" {
		return u.Username + "@" + h
	}
	return u.Username + "@localhost"
}

func EnrollLine(kp remote.Keypair) string {
	return remote.MarshalPublic(kp.Public, PublicComment())
}
