package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
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

func TestGuestProbeDiagnosticScope(t *testing.T) {
	for _, profile := range []string{"", "ubuntu-24.04-docker-v1"} {
		for _, diagnostic := range []string{"Host key verification failed SECRET_BOOTSTRAP_LOG", "Permission denied SECRET_BOOTSTRAP_LOG"} {
			t.Run(profile+"/"+strings.Fields(diagnostic)[0], func(t *testing.T) {
				lifecycleFixture(t)
				t.Setenv("HARVESTER_TEST_SSH_ERROR_OUTPUT", diagnostic)
				err := probeGuest(context.Background(), config{SSHPort: "22", SSHUser: "ubuntu", CloudInitProfile: profile}, "192.0.2.10")
				if err == nil {
					t.Fatal("probe error missing")
				}
				if got, want := strings.Contains(err.Error(), diagnostic), profile == ""; got != want {
					t.Fatal("guest diagnostic visibility differs from source mode")
				}
				if got, want := errors.Is(err, errHostKeyVerification), strings.HasPrefix(diagnostic, "Host key"); got != want {
					t.Fatal("host-key failure classification lost")
				}
			})
		}
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

func TestBundledManifestFitsKubeVirtInlineLimit(t *testing.T) {
	c := load()
	c.CloudInitProfile = "ubuntu-24.04-docker-v1"
	c.SSHUser = "developer"
	c.VMName = "proof"
	c.SSHPublicKey = syntheticPublicKey()
	data, err := resolveUserData(c)
	if err != nil {
		t.Fatal(err)
	}
	c.UserData = data
	vm, err := buildVMManifest(c, imageInfo{Format: "qcow2"})
	if err != nil {
		t.Fatal(err)
	}
	cloud := vm.Spec.Template.Spec.Volumes[len(vm.Spec.Template.Spec.Volumes)-1].CloudInitNoCloud
	if cloud["userData"] != "" {
		t.Fatal("uncompressed profile exceeds KubeVirt inline limit")
	}
	compressed, err := base64.StdEncoding.DecodeString(cloud["userDataBase64"])
	if err != nil {
		t.Fatal(err)
	}
	if len(compressed) > 2048 {
		t.Fatal("compressed profile exceeds KubeVirt inline limit")
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	decoded, err := io.ReadAll(reader)
	if err != nil || string(decoded) != data {
		t.Fatal("compressed profile did not round-trip")
	}
}

func TestBundledRejectsOversizedCompressedKeyBeforeProvisioning(t *testing.T) {
	// Deterministic public test noise, not credentials. A repeated string would compress.
	var comment strings.Builder
	for n := 0; n < 150; n++ {
		sum := sha256.Sum256([]byte(strconv.Itoa(n)))
		comment.WriteString(base64.StdEncoding.EncodeToString(sum[:]))
	}
	_, err := resolveUserData(config{CloudInitProfile: "ubuntu-24.04-docker-v1", SSHUser: "developer", VMName: "proof", SSHPublicKey: syntheticPublicKey() + comment.String()})
	if err == nil || !strings.Contains(err.Error(), "2 KiB") {
		t.Fatal("oversized compressed profile must fail before provisioning")
	}
}
