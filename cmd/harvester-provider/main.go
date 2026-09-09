package main

import (
	"bytes"
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

type config struct{ Kubeconfig, Context, Namespace, VMName, Image, ImageNS, ImageType, StorageClass, CPU, Memory, Disk, SSHHost, SSHUser, SSHPort, SSHFlags, SSHPublicKey, UserData string }

func env(n, fallback string) string {
	if v := os.Getenv(n); v != "" {
		return v
	}
	return fallback
}
func load() config {
	return config{env("KUBECONFIG", ""), os.Getenv("HARVESTER_CONTEXT"), env("HARVESTER_NAMESPACE", "default"), env("HARVESTER_VM_NAME", env("MACHINE_ID", "devsy-workspace")), os.Getenv("HARVESTER_IMAGE"), env("HARVESTER_IMAGE_NAMESPACE", "harvester-public"), env("HARVESTER_IMAGE_TYPE", "raw"), os.Getenv("HARVESTER_STORAGE_CLASS"), env("HARVESTER_CPU", "4"), env("HARVESTER_MEMORY", "8Gi"), env("HARVESTER_DISK", "40Gi"), os.Getenv("HARVESTER_SSH_HOST"), env("HARVESTER_SSH_USER", "ubuntu"), env("HARVESTER_SSH_PORT", "22"), os.Getenv("HARVESTER_SSH_FLAGS"), os.Getenv("HARVESTER_SSH_PUBLIC_KEY"), os.Getenv("HARVESTER_USER_DATA")}
}

type imageInfo struct{ Namespace, Name, StorageClass, Format string }

func parseImageReference(ref, defaultNamespace string) (imageInfo, error) {
	parts := strings.Split(ref, "/")
	if len(parts) > 2 || ref == "" {
		return imageInfo{}, errors.New("HARVESTER_IMAGE must be IMAGE or NAMESPACE/IMAGE")
	}
	if len(parts) == 1 {
		return imageInfo{Namespace: defaultNamespace, Name: parts[0]}, nil
	}
	return imageInfo{Namespace: parts[0], Name: parts[1]}, nil
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
	if c.ImageType != "raw" && c.ImageType != "qcow2" && c.ImageType != "iso" {
		return errors.New("HARVESTER_IMAGE_TYPE must be raw, qcow2, or iso")
	}
	return nil
}
func buildVMManifest(c config, image imageInfo) (vmManifest, error) {
	cpu, err := strconv.Atoi(c.CPU)
	if err != nil || cpu < 1 {
		return vmManifest{}, errors.New("HARVESTER_CPU must be a positive integer")
	}
	if c.Memory == "" || c.Disk == "" {
		return vmManifest{}, errors.New("HARVESTER_MEMORY and HARVESTER_DISK are required")
	}
	claim := c.VMName + "-rootdisk"
	if image.Format == "iso" {
		claim = c.VMName + "-blankdisk"
	}
	var m vmManifest
	m.APIVersion = "kubevirt.io/v1"
	m.Kind = "VirtualMachine"
	m.Metadata = metadata{Name: c.VMName, Namespace: c.Namespace, Labels: map[string]string{"devsy.sh/provider": "harvester", "devsy.sh/machine-id": c.VMName}}
	m.Spec.RunStrategy = "RerunOnFailure"
	m.Spec.Template.Metadata = metadata{Labels: map[string]string{"devsy.sh/provider": "harvester"}}
	m.Spec.Template.Spec.Domain = map[string]any{"cpu": map[string]int{"cores": cpu}, "memory": map[string]string{"guest": c.Memory}, "resources": map[string]any{"requests": map[string]string{"memory": c.Memory}}, "devices": map[string]any{"disks": []map[string]any{{"name": "rootdisk", "disk": map[string]string{"bus": "virtio"}}}, "interfaces": []map[string]any{{"name": "default", "bridge": map[string]any{}}}}}
	m.Spec.Template.Spec.Networks = []map[string]any{{"name": "default", "pod": map[string]any{}}}
	m.Spec.Template.Spec.Volumes = []vmVolume{{Name: "rootdisk", PersistentVolumeClaim: &struct {
		ClaimName string `json:"claimName"`
	}{ClaimName: claim}}}
	if image.Format == "iso" {
		m.Spec.Template.Spec.Volumes = append(m.Spec.Template.Spec.Volumes, vmVolume{Name: "installer", PersistentVolumeClaim: &struct {
			ClaimName string `json:"claimName"`
		}{ClaimName: c.VMName + "-image"}})
		disks := m.Spec.Template.Spec.Domain["devices"].(map[string]any)["disks"].([]map[string]any)
		delete(disks[0], "disk")
		disks[0]["cdrom"] = map[string]string{"bus": "sata"}
		disks = append(disks, map[string]any{"name": "installer", "cdrom": map[string]string{"bus": "sata"}})
		m.Spec.Template.Spec.Domain["devices"].(map[string]any)["disks"] = disks
	}
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
func buildPVCManifest(c config, image imageInfo) (pvcManifest, error) {
	if image.Name == "" || image.StorageClass == "" {
		return pvcManifest{}, errors.New("Harvester image metadata must include a storage class")
	}
	claim := c.VMName + "-rootdisk"
	annotations := map[string]string{"harvesterhci.io/imageId": image.Namespace + "/" + image.Name}
	if image.Format == "iso" {
		claim = c.VMName + "-image"
	}
	pvc := pvcManifest{APIVersion: "v1", Kind: "PersistentVolumeClaim", Metadata: metadata{Name: claim, Namespace: c.Namespace, Annotations: annotations}, Spec: map[string]any{"accessModes": []string{"ReadWriteMany"}, "resources": map[string]any{"requests": map[string]string{"storage": c.Disk}}, "storageClassName": image.StorageClass, "volumeMode": "Block"}}
	if image.Format == "iso" {
		pvc.Spec["storageClassName"] = image.StorageClass
		return pvc, nil
	}
	return pvc, nil
}
func buildBlankPVCManifest(c config, image imageInfo) pvcManifest {
	return pvcManifest{APIVersion: "v1", Kind: "PersistentVolumeClaim", Metadata: metadata{Name: c.VMName + "-blankdisk", Namespace: c.Namespace}, Spec: map[string]any{"accessModes": []string{"ReadWriteMany"}, "resources": map[string]any{"requests": map[string]string{"storage": c.Disk}}, "storageClassName": c.StorageClass, "volumeMode": "Block"}}
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
func isNotFoundError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "not found")
}

func kubectlPath() string {
	if path := os.Getenv("HARVESTER_KUBECTL_PATH"); path != "" && path != "kubectl" {
		return path
	}
	return env("KUBECTL", "kubectl")
}

func requestVMStart(c config) error {
	endpoint := fmt.Sprintf("/apis/subresources.kubevirt.io/v1/namespaces/%s/virtualmachines/%s/start", c.Namespace, c.VMName)
	_, err := kubectlInput(c, []byte("{}"), "replace", "--raw", endpoint, "-f", "-")
	return err
}

func kubectl(c config, args ...string) ([]byte, error) {
	return kubectlInput(c, nil, args...)
}
func kubectlInput(c config, input []byte, args ...string) ([]byte, error) {
	prefix := []string{}
	if c.Kubeconfig != "" {
		prefix = append(prefix, "--kubeconfig", c.Kubeconfig)
	}
	if c.Context != "" {
		prefix = append(prefix, "--context", c.Context)
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, kubectlPath(), append(prefix, args...)...)
	cmd.Stdin = bytes.NewReader(input)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return out, fmt.Errorf("kubectl: %s: %w", strings.TrimSpace(stderr.String()), err)
	}
	return out, nil
}
func resolveImage(c config) (imageInfo, error) {
	image, err := parseImageReference(c.Image, c.ImageNS)
	if err != nil {
		return image, err
	}
	out, err := kubectl(c, "get", "virtualmachineimage", image.Name, "-n", image.Namespace, "-o", "json")
	if err != nil {
		return image, err
	}
	var payload struct {
		Spec struct {
			Backend string `json:"backend"`
			URL     string `json:"url"`
		} `json:"spec"`
		Status struct {
			StorageClassName string `json:"storageClassName"`
		} `json:"status"`
	}
	if err = json.Unmarshal(out, &payload); err != nil {
		return image, err
	}
	image.StorageClass = payload.Status.StorageClassName
	if image.StorageClass == "" {
		return image, errors.New("Harvester image has no resolved storage class yet")
	}
	image.Format = c.ImageType
	if payload.Spec.Backend == "cdi" && image.Format == "raw" && strings.HasSuffix(strings.ToLower(payload.Spec.URL), ".iso") {
		image.Format = "iso"
	}
	return image, nil
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
	cmd := exec.CommandContext(ctx, kubectlPath(), args...)
	cmd.Stdin = strings.NewReader(string(payload))
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}
func apply(c config) error {
	if err := validateConfig(c, "create"); err != nil {
		return err
	}
	image, err := resolveImage(c)
	if err != nil {
		return err
	}
	pvc, err := buildPVCManifest(c, image)
	if err != nil {
		return err
	}
	if err = applyJSON(c, pvc); err != nil {
		return err
	}
	if image.Format == "iso" {
		if c.StorageClass == "" {
			return errors.New("HARVESTER_STORAGE_CLASS is required for ISO images")
		}
		if err = applyJSON(c, buildBlankPVCManifest(c, image)); err != nil {
			return err
		}
	}
	vm, err := buildVMManifest(c, image)
	if err != nil {
		return err
	}
	if err = applyJSON(c, vm); err != nil {
		return err
	}
	if err = waitForVM(c, "Running"); err != nil {
		return err
	}
	return waitForGuest(c)
}
func waitForGuest(c config) error {
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		host, err := sshHost(c)
		if err == nil {
			if err = sshRun(c, host, "command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1"); err == nil {
				return nil
			}
		}
		time.Sleep(2 * time.Second)
	}
	return errors.New("timed out waiting for SSH and Docker readiness")
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
	return sshRun(c, host, command)
}
func sshRun(c config, host, command string) error {
	x := sshExecCommand(c, host, command)
	x.Stdin, x.Stdout, x.Stderr = os.Stdin, os.Stdout, os.Stderr
	return x.Run()
}
func sshExecCommand(c config, host, command string) *exec.Cmd {
	args := []string{"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new", "-p", c.SSHPort}
	if c.SSHFlags != "" {
		args = append(args, strings.Fields(c.SSHFlags)...)
	}
	args = append(args, c.SSHUser+"@"+host, command)
	// This is Devsy's long-lived transport, not a bounded control-plane call.
	return exec.Command("ssh", args...)
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
			_, _ = kubectl(c, "delete", "pvc", c.VMName+"-image", "-n", c.Namespace, "--ignore-not-found")
			_, _ = kubectl(c, "delete", "pvc", c.VMName+"-blankdisk", "-n", c.Namespace, "--ignore-not-found")
		}
	case "start":
		err = requestVMStart(c)
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
			if isNotFoundError(e) {
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
