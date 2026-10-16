package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func init() {
	if os.Getenv("HARVESTER_TEST_LIFECYCLE") != "1" {
		return
	}
	name := strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe")
	switch name {
	case "fake-kubectl":
		args := strings.Join(os.Args[1:], " ")
		switch {
		case strings.Contains(args, "get virtualmachineimage"):
			if os.Getenv("HARVESTER_TEST_IMAGE_FAILURE") == "1" {
				os.Stderr.WriteString("image is unavailable")
				os.Exit(1)
			}
			io.WriteString(os.Stdout, `{"status":{"storageClassName":"test-image-storage"}}`)
		case strings.Contains(args, "get virtualmachine "):
			json.NewEncoder(os.Stdout).Encode(map[string]any{"status": map[string]string{"printableStatus": env("HARVESTER_TEST_VM_PHASE", "Running")}})
		case strings.Contains(args, "get vmi "):
			if os.Getenv("HARVESTER_TEST_DISCOVERY_STALL") == "1" {
				time.Sleep(time.Minute)
			}
			io.WriteString(os.Stdout, `{"status":{"interfaces":[{"interfaceName":"docker0","ipAddress":"172.17.0.1"},{"name":"default","ipAddress":"192.0.2.10"}]}}`)
		case strings.Contains(args, "get network-attachment-definition"):
			if os.Getenv("HARVESTER_TEST_NETWORK_FAILURE") == "1" {
				os.Stderr.WriteString("network attachment not found")
				os.Exit(1)
			}
		case strings.Contains(args, "apply -f -"):
			var object map[string]any
			if err := json.NewDecoder(os.Stdin).Decode(&object); err != nil {
				os.Exit(2)
			}
			fmtKind, _ := object["kind"].(string)
			io.WriteString(os.Stdout, fmtKind+" applied\n")
		}
	case "ssh":
		if os.Getenv("HARVESTER_TEST_PROXY_CHILD") == "1" {
			time.Sleep(time.Minute)
			os.Exit(0)
		}
		if file := os.Getenv("HARVESTER_TEST_PROXY_PID_FILE"); file != "" {
			child := exec.Command(os.Args[0])
			child.Env = append(os.Environ(), "HARVESTER_TEST_PROXY_CHILD=1")
			child.Stdout, child.Stderr = os.Stdout, os.Stderr
			if err := child.Start(); err != nil {
				os.Exit(2)
			}
			os.WriteFile(file, []byte(strconv.Itoa(child.Process.Pid)), 0600)
			time.Sleep(time.Minute)
		}
		if os.Getenv("HARVESTER_TEST_SSH_STALL") == "1" {
			time.Sleep(time.Minute)
		}
		command := os.Args[len(os.Args)-1]
		if !strings.Contains(command, "docker info") {
			io.WriteString(os.Stdout, "protocol\x00bytes\n")
		}
	default:
		main()
	}
	os.Exit(0)
}

func lifecycleFixture(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	for _, name := range []string{"fake-kubectl", "ssh"} {
		data, err := os.ReadFile(exe)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name+suffix), data, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HARVESTER_TEST_LIFECYCLE", "1")
	t.Setenv("HARVESTER_KUBECTL_PATH", filepath.Join(dir, "fake-kubectl"+suffix))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, name := range []string{"KUBECONFIG", "HARVESTER_CONTEXT", "HARVESTER_SSH_HOST", "HARVESTER_SSH_FLAGS", "HARVESTER_SSH_PUBLIC_KEY", "HARVESTER_USER_DATA_FILE", "HARVESTER_CLOUD_INIT_TEMPLATE", "HARVESTER_NETWORK"} {
		t.Setenv(name, "")
	}
	t.Setenv("HARVESTER_IMAGE", "test-image")
	t.Setenv("HARVESTER_IMAGE_TYPE", "qcow2")
	t.Setenv("HARVESTER_USER_DATA", "#cloud-config\nsecret-fixture-must-not-be-logged\n")
	return exe
}

func runLifecycleFixture(t *testing.T, exe string, args ...string) (string, string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func TestLifecycleProgressAndProtocolOutput(t *testing.T) {
	exe := lifecycleFixture(t)
	for _, operation := range []string{"init", "create", "start", "stop", "delete"} {
		t.Run(operation, func(t *testing.T) {
			phase := "Running"
			if operation == "stop" {
				phase = "Stopped"
			}
			t.Setenv("HARVESTER_TEST_VM_PHASE", phase)
			out, stderr, err := runLifecycleFixture(t, exe, operation)
			if err != nil {
				t.Fatalf("%s failed: %v: %s", operation, err, stderr)
			}
			if !strings.Contains(out, "harvester:") {
				t.Fatalf("%s emitted no lifecycle progress: %q", operation, out)
			}
			if strings.Contains(out+stderr, "secret-fixture-must-not-be-logged") {
				t.Fatal("cloud-init content leaked into progress output")
			}
			if operation == "create" {
				previous := -1
				for _, stage := range []string{"Resolving cloud-init", "Resolving VM image", "Provisioning root disk", "Creating virtual machine", "Waiting for virtual machine", "Waiting for guest IP", "Guest IP available", "SSH and Docker are ready", "Machine ready"} {
					index := strings.Index(out, stage)
					if index <= previous {
						t.Fatalf("missing or out-of-order stage %q: %s", stage, out)
					}
					previous = index
				}
			}
		})
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"status"}, "Running"},
		{[]string{"command", "--command", "protocol-fixture"}, "protocol\x00bytes\n"},
	} {
		out, stderr, err := runLifecycleFixture(t, exe, tc.args...)
		if err != nil || out != tc.want || stderr != "" {
			t.Fatalf("protocol output changed for %v: stdout=%q stderr=%q err=%v", tc.args, out, stderr, err)
		}
	}
}

func TestCreateProgressStopsAtFailedStage(t *testing.T) {
	exe := lifecycleFixture(t)
	t.Setenv("HARVESTER_TEST_IMAGE_FAILURE", "1")
	out, stderr, err := runLifecycleFixture(t, exe, "create")
	if err == nil || !strings.Contains(stderr, "image is unavailable") {
		t.Fatalf("expected the image lookup failure: %v %q", err, stderr)
	}
	if !strings.Contains(out, "Resolving VM image") || strings.Contains(out, "Creating virtual machine") || strings.Contains(out, "Machine ready") {
		t.Fatalf("progress misreported a failed create: %q", out)
	}
}

func TestSSHHostUsesPrimaryKubeVirtIPAddress(t *testing.T) {
	lifecycleFixture(t)
	host, err := sshHost(config{VMName: "machine-1", Namespace: "default"})
	if err != nil || host != "192.0.2.10" {
		t.Fatalf("expected default-network ipAddress, not a guest Docker bridge: host=%q err=%v", host, err)
	}
}

func TestConfiguredHarvesterNetwork(t *testing.T) {
	t.Setenv("HARVESTER_NETWORK", "default/mgmt-untagged")
	c := load()
	vm, err := buildVMManifest(c, imageInfo{Format: "qcow2"})
	if err != nil {
		t.Fatal(err)
	}
	network := vm.Spec.Template.Spec.Networks[0]
	multus, ok := network["multus"].(map[string]string)
	if !ok || multus["networkName"] != "default/mgmt-untagged" || network["pod"] != nil {
		t.Fatalf("configured network was not attached: %#v", network)
	}
	t.Setenv("HARVESTER_NETWORK", "")
	vm, err = buildVMManifest(load(), imageInfo{Format: "qcow2"})
	if err != nil || vm.Spec.Template.Spec.Networks[0]["pod"] == nil {
		t.Fatalf("default pod network changed: %#v %v", vm.Spec.Template.Spec.Networks, err)
	}
}

func TestInvalidNetworkFailsBeforeProvisioning(t *testing.T) {
	exe := lifecycleFixture(t)
	for _, network := range []string{"a/b/c", "/missing-namespace", "default/"} {
		t.Setenv("HARVESTER_NETWORK", network)
		out, _, err := runLifecycleFixture(t, exe, "create")
		if err == nil || strings.Contains(out, "Provisioning root disk") {
			t.Fatalf("invalid network %q reached provisioning: %q %v", network, out, err)
		}
	}
	t.Setenv("HARVESTER_NETWORK", "default/mgmt-untagged")
	t.Setenv("HARVESTER_TEST_NETWORK_FAILURE", "1")
	for _, operation := range []string{"init", "create"} {
		out, stderr, err := runLifecycleFixture(t, exe, operation)
		if err == nil || !strings.Contains(stderr, "network attachment not found") || strings.Contains(out, "Provisioning root disk") || strings.Contains(out, "Provider initialized") {
			t.Fatalf("missing network was accepted for %s: %q %q %v", operation, out, stderr, err)
		}
	}
}

func TestGuestReadinessProbeHonorsDeadline(t *testing.T) {
	lifecycleFixture(t)
	t.Setenv("HARVESTER_TEST_SSH_STALL", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := probeGuest(ctx, config{SSHPort: "22", SSHUser: "ubuntu"}, "192.0.2.10")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stalled SSH probe did not report its deadline: %v", err)
	}
	if time.Since(started) > 5*time.Second {
		t.Fatal("stalled SSH probe exceeded its deadline by more than 5 seconds")
	}
}

func TestGuestIPDiscoveryHonorsReadinessDeadline(t *testing.T) {
	lifecycleFixture(t)
	t.Setenv("HARVESTER_TEST_DISCOVERY_STALL", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := sshHostContext(ctx, config{VMName: "machine-1", Namespace: "default"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stalled discovery ignored readiness deadline: %v", err)
	}
}

func TestGuestReadinessProbeBoundsProxyPipeCleanup(t *testing.T) {
	lifecycleFixture(t)
	file := filepath.Join(t.TempDir(), "proxy.pid")
	t.Setenv("HARVESTER_TEST_PROXY_PID_FILE", file)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- probeGuest(ctx, config{SSHPort: "22", SSHUser: "ubuntu"}, "192.0.2.10") }()
	defer func() {
		if data, err := os.ReadFile(file); err == nil {
			pid, _ := strconv.Atoi(string(data))
			if pid > 0 {
				if child, err := os.FindProcess(pid); err == nil {
					_ = child.Kill()
					_, _ = child.Wait()
				}
			}
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if data, err := os.ReadFile(file); err == nil && len(data) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("SSH fixture did not publish its proxy child")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("unexpected probe result after cancellation: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("proxy-held output pipes blocked readiness cancellation")
	}
}
