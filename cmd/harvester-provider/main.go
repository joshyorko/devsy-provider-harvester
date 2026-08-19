package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const commandTimeout = 2 * time.Minute

type config struct{ Kubeconfig, Context, Namespace, VMName, Image, ImageNS, CPU, Memory, Disk, SSHHost, SSHUser, SSHPort, SSHFlags, SSHPublicKey, UserData string }

func env(n, fallback string) string {
	if v := os.Getenv(n); v != "" {
		return v
	}
	return fallback
}
func load() config {
	return config{env("KUBECONFIG", ""), os.Getenv("HARVESTER_CONTEXT"), env("HARVESTER_NAMESPACE", "default"), env("HARVESTER_VM_NAME", env("MACHINE_ID", "devsy-workspace")), os.Getenv("HARVESTER_IMAGE"), env("HARVESTER_IMAGE_NAMESPACE", "harvester-public"), env("HARVESTER_CPU", "4"), env("HARVESTER_MEMORY", "8Gi"), env("HARVESTER_DISK", "40Gi"), os.Getenv("HARVESTER_SSH_HOST"), env("HARVESTER_SSH_USER", "ubuntu"), env("HARVESTER_SSH_PORT", "22"), os.Getenv("HARVESTER_SSH_FLAGS"), os.Getenv("HARVESTER_SSH_PUBLIC_KEY"), os.Getenv("HARVESTER_USER_DATA")}
}

type metadata struct {
	Name        string            `json:"name"`
	Namespace   string            `json:"namespace"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}
type vmVolume struct {
	Name                  string `json:"name"`
	PersistentVolumeClaim *struct {
		ClaimName string `json:"claimName"`
	} `json:"persistentVolumeClaim,omitempty"`
	CloudInitNoCloud map[string]string `json:"cloudInitNoCloud,omitempty"`
}
type vmManifest struct {
	APIVersion string   `json:"apiVersion"`
	Kind       string   `json:"kind"`
	Metadata   metadata `json:"metadata"`
	Spec       struct {
		RunStrategy string `json:"runStrategy"`
		Running     *bool  `json:"running,omitempty"`
		Template    struct {
			Metadata metadata `json:"metadata"`
			Spec     struct {
				Domain   map[string]any   `json:"domain"`
				Networks []map[string]any `json:"networks"`
				Volumes  []vmVolume       `json:"volumes"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
}
type pvcManifest struct {
	APIVersion string         `json:"apiVersion"`
	Kind       string         `json:"kind"`
	Metadata   metadata       `json:"metadata"`
	Spec       map[string]any `json:"spec"`
}
type vmStatus struct {
	Ready bool
	Phase string
}

func validateConfig(c config, operation string) error {
	if operation == "create" && c.Image == "" {
		return errors.New("HARVESTER_IMAGE is required")
	}
	if operation == "create" && c.SSHPublicKey == "" && c.UserData == "" && c.SSHHost == "" {
		return errors.New("HARVESTER_SSH_PUBLIC_KEY or HARVESTER_USER_DATA is required when SSH host discovery is enabled")
	}
	if _, err := strconv.Atoi(c.SSHPort); err != nil {
		return fmt.Errorf("HARVESTER_SSH_PORT must be numeric: %w", err)
	}
	return nil
}
func buildVMManifest(c config) (vmManifest, error) {
	cpu, err := strconv.Atoi(c.CPU)
	if err != nil || cpu < 1 {
		return vmManifest{}, errors.New("HARVESTER_CPU must be a positive integer")
	}
	if c.Memory == "" || c.Disk == "" {
		return vmManifest{}, errors.New("HARVESTER_MEMORY and HARVESTER_DISK are required")
	}
	claim := c.VMName + "-rootdisk"
	var m vmManifest
	m.APIVersion = "kubevirt.io/v1"
	m.Kind = "VirtualMachine"
	m.Metadata = metadata{Name: c.VMName, Namespace: c.Namespace, Labels: map[string]string{"devsy.sh/provider": "harvester", "devsy.sh/machine-id": c.VMName}}
	m.Spec.RunStrategy = "RerunOnFailure"
	m.Spec.Template.Metadata = metadata{Labels: map[string]string{"devsy.sh/provider": "harvester"}}
	m.Spec.Template.Spec.Domain = map[string]any{"cpu": map[string]int{"cores": cpu}, "resources": map[string]any{"requests": map[string]string{"memory": c.Memory}}, "devices": map[string]any{"disks": []map[string]any{{"name": "rootdisk", "disk": map[string]string{"bus": "virtio"}}}, "interfaces": []map[string]any{{"name": "default", "bridge": map[string]any{}}}}}
	m.Spec.Template.Spec.Networks = []map[string]any{{"name": "default", "pod": map[string]any{}}}
	m.Spec.Template.Spec.Volumes = []vmVolume{{Name: "rootdisk", PersistentVolumeClaim: &struct {
		ClaimName string `json:"claimName"`
	}{ClaimName: claim}}}
	userData := c.UserData
	if userData == "" && c.SSHPublicKey != "" {
		userData = "#cloud-config\nusers:\n  - name: " + c.SSHUser + "\n    sudo: ALL=(ALL) NOPASSWD:ALL\n    shell: /bin/bash\n    ssh_authorized_keys:\n      - " + c.SSHPublicKey + "\n"
	}
	if userData != "" {
		m.Spec.Template.Spec.Volumes = append(m.Spec.Template.Spec.Volumes, vmVolume{Name: "cloudinit", CloudInitNoCloud: map[string]string{"userData": userData}})
		m.Spec.Template.Spec.Domain["devices"].(map[string]any)["disks"] = append(m.Spec.Template.Spec.Domain["devices"].(map[string]any)["disks"].([]map[string]any), map[string]any{"name": "cloudinit", "disk": map[string]string{"bus": "virtio"}})
	}
	return m, nil
}
func buildPVCManifest(c config) (pvcManifest, error) {
	if c.Image == "" {
		return pvcManifest{}, errors.New("HARVESTER_IMAGE is required")
	}
	image := c.Image
	if !strings.Contains(image, "/") {
		image = c.ImageNS + "/" + image
	}
	return pvcManifest{APIVersion: "v1", Kind: "PersistentVolumeClaim", Metadata: metadata{Name: c.VMName + "-rootdisk", Namespace: c.Namespace, Annotations: map[string]string{"harvesterhci.io/imageId": image}}, Spec: map[string]any{"accessModes": []string{"ReadWriteMany"}, "resources": map[string]any{"requests": map[string]string{"storage": c.Disk}}}}, nil
}
func statusForVM(s *vmStatus, notFound bool) string {
	if notFound || s == nil {
		return "NotFound"
	}
	switch strings.ToLower(s.Phase) {
	case "running":
		return "Running"
	case "stopped", "halted":
		return "Stopped"
	default:
		if s.Ready {
			return "Running"
		}
		return "Busy"
	}
}

func kubectl(c config, args ...string) ([]byte, error) {
	prefix := []string{}
	if c.Kubeconfig != "" {
		prefix = append(prefix, "--kubeconfig", c.Kubeconfig)
	}
	if c.Context != "" {
		prefix = append(prefix, "--context", c.Context)
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "kubectl", append(prefix, args...)...)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return out, err
}
func applyJSON(c config, object any) error {
	payload, err := json.Marshal(object)
	if err != nil {
		return err
	}
	args := []string{}
	if c.Kubeconfig != "" {
		args = append(args, "--kubeconfig", c.Kubeconfig)
	}
	if c.Context != "" {
		args = append(args, "--context", c.Context)
	}
	args = append(args, "apply", "-f", "-")
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	cmd.Stdin = strings.NewReader(string(payload))
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}
func apply(c config) error {
	if err := validateConfig(c, "create"); err != nil {
		return err
	}
	pvc, err := buildPVCManifest(c)
	if err != nil {
		return err
	}
	if err = applyJSON(c, pvc); err != nil {
		return err
	}
	vm, err := buildVMManifest(c)
	if err != nil {
		return err
	}
	if err = applyJSON(c, vm); err != nil {
		return err
	}
	return waitForVM(c, "Running")
}
func readVMStatus(c config) (*vmStatus, error) {
	out, err := kubectl(c, "get", "virtualmachine", c.VMName, "-n", c.Namespace, "-o", "json")
	if err != nil {
		return nil, err
	}
	var p struct {
		Status struct {
			Ready           bool   `json:"ready"`
			PrintableStatus string `json:"printableStatus"`
		} `json:"status"`
	}
	if err := json.Unmarshal(out, &p); err != nil {
		return nil, err
	}
	return &vmStatus{Ready: p.Status.Ready, Phase: p.Status.PrintableStatus}, nil
}
func waitForVM(c config, desired string) error {
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		status, err := readVMStatus(c)
		if err == nil && statusForVM(status, false) == desired {
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("timed out waiting for VM %s to become %s", c.VMName, desired)
}
func sshHost(c config) (string, error) {
	if c.SSHHost != "" {
		return c.SSHHost, nil
	}
	out, err := kubectl(c, "get", "vmi", c.VMName, "-n", c.Namespace, "-o", "json")
	if err != nil {
		return "", err
	}
	var p struct {
		Status struct {
			Interfaces []struct {
				IP string `json:"ip"`
			} `json:"interfaces"`
		} `json:"status"`
	}
	if err = json.Unmarshal(out, &p); err != nil {
		return "", err
	}
	for _, i := range p.Status.Interfaces {
		if i.IP != "" {
			return i.IP, nil
		}
	}
	return "", errors.New("VM has no network IP yet")
}
func sshCommand(c config, command string) error {
	host, err := sshHost(c)
	if err != nil {
		return err
	}
	args := []string{"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new", "-p", c.SSHPort}
	if c.SSHFlags != "" {
		args = append(args, strings.Fields(c.SSHFlags)...)
	}
	args = append(args, c.SSHUser+"@"+host, command)
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	x := exec.CommandContext(ctx, "ssh", args...)
	x.Stdin, x.Stdout, x.Stderr = os.Stdin, os.Stdout, os.Stderr
	return x.Run()
}

func main() {
	c := load()
	op := "command"
	if len(os.Args) > 1 {
		op = os.Args[1]
	}
	var err error
	switch op {
	case "init":
		_, err = kubectl(c, "version", "--request-timeout=10s")
	case "create":
		err = apply(c)
	case "delete":
		_, err = kubectl(c, "delete", "virtualmachine", c.VMName, "-n", c.Namespace, "--ignore-not-found")
		if err == nil {
			_, err = kubectl(c, "delete", "pvc", c.VMName+"-rootdisk", "-n", c.Namespace, "--ignore-not-found")
		}
	case "start":
		_, err = kubectl(c, "patch", "virtualmachine", c.VMName, "-n", c.Namespace, "--type=merge", "-p", `{"spec":{"runStrategy":"RerunOnFailure"}}`)
		if err == nil {
			err = waitForVM(c, "Running")
		}
	case "stop":
		_, err = kubectl(c, "patch", "virtualmachine", c.VMName, "-n", c.Namespace, "--type=merge", "-p", `{"spec":{"runStrategy":"Halted"}}`)
		if err == nil {
			err = waitForVM(c, "Stopped")
		}
	case "status":
		out, e := kubectl(c, "get", "virtualmachine", c.VMName, "-n", c.Namespace, "-o", "json")
		if e != nil {
			if strings.Contains(strings.ToLower(e.Error()), "not found") {
				fmt.Print("NotFound")
				return
			}
			fmt.Fprintln(os.Stderr, e)
			os.Exit(1)
		}
		var p struct {
			Status struct {
				Ready           bool   `json:"ready"`
				PrintableStatus string `json:"printableStatus"`
			} `json:"status"`
		}
		if e = json.Unmarshal(out, &p); e != nil {
			fmt.Fprintln(os.Stderr, e)
			os.Exit(1)
		}
		fmt.Print(statusForVM(&vmStatus{Ready: p.Status.Ready, Phase: p.Status.PrintableStatus}, false))
		return
	case "command":
		fs := flag.NewFlagSet("command", flag.ExitOnError)
		command := fs.String("command", "", "")
		_ = fs.Parse(os.Args[2:])
		err = sshCommand(c, *command)
	default:
		fmt.Fprintln(os.Stderr, "usage: harvester-provider {init|create|delete|start|stop|status|command}")
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
