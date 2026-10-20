package main

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func syntheticPublicKey() string {
	b := []byte{}
	for _, part := range [][]byte{[]byte("ssh-ed25519"), make([]byte, 32)} {
		b = binary.BigEndian.AppendUint32(b, uint32(len(part)))
		b = append(b, part...)
	}
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(b) + " synthetic-test-only \"quoted\": [comment] # inert"
}

func TestBundledProfile(t *testing.T) {
	t.Setenv("HARVESTER_CLOUD_INIT_PROFILE", "ubuntu-24.04-docker-v1")
	t.Setenv("HARVESTER_SSH_USER", "developer")
	t.Setenv("HARVESTER_VM_NAME", "dev-test")
	t.Setenv("HARVESTER_SSH_PUBLIC_KEY", syntheticPublicKey())
	c := load()
	data, err := resolveUserData(c)
	if err != nil || !strings.Contains(data, "docker-ce-cli") || !strings.Contains(data, `name: "developer"`) || !strings.Contains(data, `hostname: "dev-test"`) {
		t.Fatalf("profile not rendered: %v", err)
	}
	for _, setter := range []func(*config){func(c *config) { c.UserData = "SECRET" }, func(c *config) { c.UserDataFile = "SECRET" }, func(c *config) { c.CloudInitTemplate = "SECRET" }} {
		conflict := c
		setter(&conflict)
		_, err := resolveUserData(conflict)
		if err == nil || strings.Contains(err.Error(), "SECRET") {
			t.Fatal("source conflict not safely rejected")
		}
	}
	for _, variable := range []string{"HARVESTER_SSH_USER", "HARVESTER_VM_NAME", "HARVESTER_SSH_PUBLIC_KEY", "HARVESTER_CLOUD_INIT_PROFILE"} {
		t.Run(variable, func(t *testing.T) {
			t.Setenv(variable, "SECRET\nruncmd: [bad]")
			_, err := resolveUserData(load())
			if err == nil || strings.Contains(err.Error(), "SECRET") {
				t.Fatal("invalid input not safely rejected")
			}
		})
	}
	for _, user := range []string{"root", "", "-option", "name with spaces"} {
		bad := c
		bad.SSHUser = user
		if _, err := resolveUserData(bad); err == nil {
			t.Fatal("unsafe user accepted")
		}
	}
	for _, key := range []string{"", "ssh-ed25519 YQ==", syntheticPublicKey() + "\nssh-rsa AAAA"} {
		bad := c
		bad.SSHPublicKey = key
		if _, err := resolveUserData(bad); err == nil {
			t.Fatal("invalid key accepted")
		}
	}
}

func TestBundledReadinessBudget(t *testing.T) {
	t.Setenv("HARVESTER_CLOUD_INIT_PROFILE", "ubuntu-24.04-docker-v1")
	if guestReadyTimeout(load()) != 15*time.Minute {
		t.Fatal("first boot needs bounded package installation budget")
	}
	t.Setenv("HARVESTER_CLOUD_INIT_PROFILE", "")
	if guestReadyTimeout(load()) != 90*time.Second {
		t.Fatal("custom source readiness changed")
	}
}

// Export only synthetic data for the container schema check; never operator config.
func TestBundledSchemaFixture(t *testing.T) {
	path := os.Getenv("HARVESTER_TEST_SCHEMA_OUTPUT")
	if path == "" {
		t.Skip("schema fixture export not requested")
	}
	data, err := renderBundledProfile(config{CloudInitProfile: "ubuntu-24.04-docker-v1", SSHUser: "developer", VMName: "devsy-schema-test", SSHPublicKey: syntheticPublicKey()})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestBundledProbeDoesNotExposeGuestOutput(t *testing.T) {
	lifecycleFixture(t)
	t.Setenv("HARVESTER_TEST_SSH_ERROR_OUTPUT", "Host key verification failed SECRET_BOOTSTRAP_LOG")
	err := probeGuest(context.Background(), config{SSHPort: "22", SSHUser: "ubuntu", CloudInitProfile: "ubuntu-24.04-docker-v1"}, "192.0.2.10")
	if err == nil || strings.Contains(err.Error(), "SECRET_BOOTSTRAP_LOG") {
		t.Fatal("probe leaked guest output")
	}
}

func TestBundledProbeWaitsForCloudInitAndComponents(t *testing.T) {
	lifecycleFixture(t)
	path := filepath.Join(t.TempDir(), "ssh-calls.json")
	t.Setenv("HARVESTER_TEST_SSH_CALLS", path)
	if err := probeGuest(context.Background(), config{SSHPort: "22", SSHUser: "ubuntu", CloudInitProfile: "ubuntu-24.04-docker-v1"}, "192.0.2.10"); err != nil {
		t.Fatal(err)
	}
	calls, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []string{"cloud-init status --wait", "docker info", "docker compose version", "docker buildx version", "systemctl is-active --quiet qemu-guest-agent"} {
		if !strings.Contains(string(calls), check) {
			t.Fatalf("missing readiness check: %s", check)
		}
	}
}

func TestBundledBootstrapHasHardDeadline(t *testing.T) {
	data, err := renderBundledProfile(config{CloudInitProfile: "ubuntu-24.04-docker-v1", SSHUser: "developer", VMName: "test", SSHPublicKey: syntheticPublicKey()})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(data, "[timeout, '--kill-after=30', '840', /usr/local/sbin/devsy-docker-bootstrap]") {
		t.Fatal("bootstrap must terminate processes that ignore TERM within readiness budget")
	}
}
