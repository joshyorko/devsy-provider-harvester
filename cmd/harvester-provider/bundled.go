package main

import (
	"bytes"
	_ "embed"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"text/template"
	"time"
)

//go:embed profiles/ubuntu-24.04-docker-v1.yaml
var ubuntuDockerProfile string

func renderBundledProfile(c config) (string, error) {
	if c.CloudInitProfile != "ubuntu-24.04-docker-v1" {
		return "", errors.New("HARVESTER_CLOUD_INIT_PROFILE must be ubuntu-24.04-docker-v1")
	}
	if c.SSHUser == "root" || !regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`).MatchString(c.SSHUser) {
		return "", errors.New("bundled profile requires a valid non-root HARVESTER_SSH_USER")
	}
	if len(c.VMName) > 63 || !regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`).MatchString(c.VMName) {
		return "", errors.New("bundled profile requires HARVESTER_VM_NAME to be a DNS hostname of at most 63 characters")
	}
	if !validPublicKey(c.SSHPublicKey) {
		return "", errors.New("bundled profile requires one valid Ed25519, RSA, or ECDSA HARVESTER_SSH_PUBLIC_KEY")
	}
	t, err := template.New("ubuntu-docker").Funcs(template.FuncMap{"quote": strconv.Quote}).Parse(ubuntuDockerProfile)
	if err != nil {
		return "", errors.New("invalid embedded cloud-init profile")
	}
	var out bytes.Buffer
	if err = t.Execute(&out, c); err != nil {
		return "", errors.New("could not render embedded cloud-init profile")
	}
	return out.String(), nil
}

func validPublicKey(key string) bool {
	if strings.ContainsAny(key, "\r\n\x00") || len(key) > 16384 {
		return false
	}
	fields := strings.Fields(key)
	if len(fields) < 2 {
		return false
	}
	wire, err := base64.StdEncoding.DecodeString(fields[1])
	if err != nil {
		return false
	}
	var parts [][]byte
	for len(wire) > 0 {
		if len(wire) < 4 {
			return false
		}
		n := binary.BigEndian.Uint32(wire)
		wire = wire[4:]
		if uint64(n) > uint64(len(wire)) {
			return false
		}
		parts = append(parts, wire[:n])
		wire = wire[n:]
	}
	if len(parts) < 2 || string(parts[0]) != fields[0] {
		return false
	}
	switch fields[0] {
	case "ssh-ed25519":
		return len(parts) == 2 && len(parts[1]) == 32
	case "ssh-rsa":
		return len(parts) == 3 && len(parts[1]) > 0 && len(parts[2]) >= 256
	case "ecdsa-sha2-nistp256", "ecdsa-sha2-nistp384", "ecdsa-sha2-nistp521":
		sizes := map[string]int{"nistp256": 65, "nistp384": 97, "nistp521": 133}
		return len(parts) == 3 && fields[0] == "ecdsa-sha2-"+string(parts[1]) && len(parts[2]) == sizes[string(parts[1])] && parts[2][0] == 4
	}
	return false
}

func guestReadyTimeout(c config) time.Duration {
	if c.CloudInitProfile != "" {
		return 15 * time.Minute
	}
	return 90 * time.Second
}
