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

var errHostKeyVerification = errors.New("SSH host key verification failed; verify the VM's host key before updating its known_hosts entry")

type config struct{ Kubeconfig, Context, Namespace, VMName, Image, ImageNS, ImageType, StorageClass, CPU, Memory, Disk, SSHHost, SSHUser, SSHPort, SSHFlags, SSHPublicKey, UserData, UserDataFile, CloudInitTemplate, Network string }

func env(n, fallback string) string {
	if v := os.Getenv(n); v != "" {
		return v
	}
	return fallback
}
func load() config {
	return config{env("KUBECONFIG", ""), os.Getenv("HARVESTER_CONTEXT"), env("HARVESTER_NAMESPACE", "default"), env("HARVESTER_VM_NAME", env("MACHINE_ID", "devsy-workspace")), os.Getenv("HARVESTER_IMAGE"), env("HARVESTER_IMAGE_NAMESPACE", "harvester-public"), env("HARVESTER_IMAGE_TYPE", "raw"), os.Getenv("HARVESTER_STORAGE_CLASS"), env("HARVESTER_CPU", "4"), env("HARVESTER_MEMORY", "8Gi"), env("HARVESTER_DISK", "40Gi"), os.Getenv("HARVESTER_SSH_HOST"), env("HARVESTER_SSH_USER", "ubuntu"), env("HARVESTER_SSH_PORT", "22"), os.Getenv("HARVESTER_SSH_FLAGS"), os.Getenv("HARVESTER_SSH_PUBLIC_KEY"), os.Getenv("HARVESTER_USER_DATA"), os.Getenv("HARVESTER_USER_DATA_FILE"), os.Getenv("HARVESTER_CLOUD_INIT_TEMPLATE"), os.Getenv("HARVESTER_NETWORK")}
}

func progress(message string) {
	fmt.Fprintln(os.Stdout, "harvester: "+message)
}

func checkNetwork(c config) error {
	if c.Network == "" {
		return nil
	}
	namespace, name := c.Namespace, c.Network
	parts := strings.Split(c.Network, "/")
	if len(parts) == 2 {
		namespace, name = parts[0], parts[1]
	}
	if len(parts) > 2 || namespace == "" || name == "" {
		return errors.New("HARVESTER_NETWORK must be NAME or NAMESPACE/NAME")
	}
	progress("Checking VM network attachment")
	_, err := kubectl(c, "get", "network-attachment-definition", name, "-n", namespace, "-o", "name", "--request-timeout=10s")
	return err
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
	Name              string            `json:"name"`
	Namespace         string            `json:"namespace"`
	Labels            map[string]string `json:"labels,omitempty"`
	Annotations       map[string]string `json:"annotations,omitempty"`
	DeletionTimestamp string            `json:"deletionTimestamp,omitempty"`
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
		return errors.New("HARVESTER_SSH_PUBLIC_KEY or a cloud-init source (HARVESTER_USER_DATA, HARVESTER_USER_DATA_FILE, HARVESTER_CLOUD_INIT_TEMPLATE) is required when SSH host discovery is enabled")
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
	m.Spec.Template.Spec.Domain = map[string]any{"cpu": map[string]int{"cores": cpu}, "memory": map[string]string{"guest": c.Memory}, "resources": map[string]any{"requests": map[string]string{"memory": c.Memory}, "limits": map[string]string{"memory": c.Memory}}, "devices": map[string]any{"disks": []map[string]any{{"name": "rootdisk", "disk": map[string]string{"bus": "virtio"}}}, "interfaces": []map[string]any{{"name": "default", "bridge": map[string]any{}}}}}
	m.Spec.Template.Spec.Networks = []map[string]any{{"name": "default", "pod": map[string]any{}}}
	if c.Network != "" {
		m.Spec.Template.Spec.Networks = []map[string]any{{"name": "default", "multus": map[string]string{"networkName": c.Network}}}
	}
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
	pvc := pvcManifest{APIVersion: "v1", Kind: "PersistentVolumeClaim", Metadata: metadata{Name: claim, Namespace: c.Namespace, Annotations: annotations, Labels: map[string]string{"devsy.sh/provider": "harvester", "devsy.sh/machine-id": c.VMName}}, Spec: map[string]any{"accessModes": []string{"ReadWriteMany"}, "resources": map[string]any{"requests": map[string]string{"storage": c.Disk}}, "storageClassName": image.StorageClass, "volumeMode": "Block"}}
	if image.Format == "iso" {
		pvc.Spec["storageClassName"] = image.StorageClass
		return pvc, nil
	}
	return pvc, nil
}
func buildBlankPVCManifest(c config, image imageInfo) pvcManifest {
	return pvcManifest{APIVersion: "v1", Kind: "PersistentVolumeClaim", Metadata: metadata{Name: c.VMName + "-blankdisk", Namespace: c.Namespace, Labels: map[string]string{"devsy.sh/provider": "harvester", "devsy.sh/machine-id": c.VMName}}, Spec: map[string]any{"accessModes": []string{"ReadWriteMany"}, "resources": map[string]any{"requests": map[string]string{"storage": c.Disk}}, "storageClassName": c.StorageClass, "volumeMode": "Block"}}
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
	return kubectlInputContext(context.Background(), c, input, args...)
}

func kubectlInputContext(parent context.Context, c config, input []byte, args ...string) ([]byte, error) {
	prefix := []string{}
	if c.Kubeconfig != "" {
		prefix = append(prefix, "--kubeconfig", c.Kubeconfig)
	}
	if c.Context != "" {
		prefix = append(prefix, "--context", c.Context)
	}
	ctx, cancel := context.WithTimeout(parent, commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, kubectlPath(), append(prefix, args...)...)
	cmd.WaitDelay = time.Second
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
func createJSON(c config, object any) error {
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
	args = append(args, "create", "-f", "-")
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, kubectlPath(), args...)
	cmd.Stdin = strings.NewReader(string(payload))
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}
func apply(c config) error {
	reused, err := reuseExistingVM(c)
	if err != nil {
		return err
	}
	if !reused {
		if err := provisionVM(c); err != nil {
			return err
		}
	}
	if err := waitForVM(c, "Running"); err != nil {
		return err
	}
	if err := waitForGuest(c); err != nil {
		return err
	}
	progress("Machine ready")
	return nil
}

func reuseExistingVM(c config) (bool, error) {
	out, err := kubectl(c, "get", "virtualmachine", c.VMName, "-n", c.Namespace, "-o", "json", "--ignore-not-found")
	if err != nil {
		return false, err
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return false, nil
	}
	var vm struct {
		Metadata metadata `json:"metadata"`
		Status   struct {
			Phase string `json:"printableStatus"`
		} `json:"status"`
	}
	if err := json.Unmarshal(out, &vm); err != nil {
		return false, fmt.Errorf("decode existing VM %s: %w", c.VMName, err)
	}
	if vm.Metadata.Labels["devsy.sh/provider"] != "harvester" || vm.Metadata.Labels["devsy.sh/machine-id"] != c.VMName {
		return false, fmt.Errorf("existing VM %s is not owned by this Harvester machine; refusing to reuse or overwrite it", c.VMName)
	}
	if vm.Metadata.DeletionTimestamp != "" {
		return false, fmt.Errorf("VM %s is deleting; wait for deletion to finish before retrying", c.VMName)
	}
	progress("Reusing existing virtual machine without changing its VM or disk definitions")
	if strings.EqualFold(vm.Status.Phase, "Stopped") || strings.EqualFold(vm.Status.Phase, "Halted") {
		progress("Requesting existing virtual machine start")
		if err := requestVMStart(c); err != nil {
			return false, err
		}
	}
	return true, nil
}

func ensurePVC(c config, pvc pvcManifest) error {
	out, err := kubectl(c, "get", "pvc", pvc.Metadata.Name, "-n", c.Namespace, "-o", "json", "--ignore-not-found")
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return createJSON(c, pvc)
	}
	var existing struct {
		Metadata metadata `json:"metadata"`
		Spec     struct {
			StorageClassName string   `json:"storageClassName"`
			VolumeMode       string   `json:"volumeMode"`
			AccessModes      []string `json:"accessModes"`
			Resources        struct {
				Requests map[string]string `json:"requests"`
			} `json:"resources"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(out, &existing); err != nil {
		return fmt.Errorf("decode existing disk claim %s: %w", pvc.Metadata.Name, err)
	}
	if existing.Metadata.Labels["devsy.sh/provider"] != "harvester" || existing.Metadata.Labels["devsy.sh/machine-id"] != c.VMName || existing.Metadata.DeletionTimestamp != "" {
		return fmt.Errorf("disk claim %s is not reusable: ownership labels must match and deletion must not be in progress", pvc.Metadata.Name)
	}
	if existing.Metadata.Annotations["harvesterhci.io/imageId"] != pvc.Metadata.Annotations["harvesterhci.io/imageId"] ||
		existing.Spec.StorageClassName != pvc.Spec["storageClassName"] || existing.Spec.VolumeMode != "Block" ||
		len(existing.Spec.AccessModes) != 1 || existing.Spec.AccessModes[0] != "ReadWriteMany" || existing.Spec.Resources.Requests["storage"] != c.Disk {
		return fmt.Errorf("disk claim %s does not match the requested image, storage class, size or mode; refusing to modify it", pvc.Metadata.Name)
	}
	progress("Reusing existing disk claim " + pvc.Metadata.Name + " without changing its definition")
	return nil
}

func provisionVM(c config) error {
	progress("Resolving cloud-init configuration")
	userData, err := resolveUserData(c)
	if err != nil {
		return err
	}
	c.UserData = userData
	if err := validateConfig(c, "create"); err != nil {
		return err
	}
	if err := checkNetwork(c); err != nil {
		return err
	}
	progress("Resolving VM image")
	image, err := resolveImage(c)
	if err != nil {
		return err
	}
	pvc, err := buildPVCManifest(c, image)
	if err != nil {
		return err
	}
	progress("Provisioning root disk")
	if err = ensurePVC(c, pvc); err != nil {
		return err
	}
	if image.Format == "iso" {
		if c.StorageClass == "" {
			return errors.New("HARVESTER_STORAGE_CLASS is required for ISO images")
		}
		if err = ensurePVC(c, buildBlankPVCManifest(c, image)); err != nil {
			return err
		}
	}
	progress("Creating virtual machine")
	vm, err := buildVMManifest(c, image)
	if err != nil {
		return err
	}
	// An intervening create must fail rather than overwrite another VM's spec.
	return createJSON(c, vm)
}
func waitForGuest(c config) error {
	progress("Waiting for guest IP, SSH and Docker readiness")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	var lastErr error
	ipReported := false
	for ctx.Err() == nil {
		host, err := sshHostContext(ctx, c)
		if err == nil {
			if !ipReported {
				progress("Guest IP available; checking SSH and Docker")
				ipReported = true
			}
			probeCtx, probeCancel := context.WithTimeout(ctx, 10*time.Second)
			err = probeGuest(probeCtx, c, host)
			probeCancel()
			if err == nil {
				progress("SSH and Docker are ready")
				return nil
			}
			if errors.Is(err, errHostKeyVerification) {
				return err
			}
		}
		lastErr = err
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("timed out waiting for SSH and Docker readiness; check that the VM network is reachable from this client and cloud-init installed Docker: %w", lastErr)
}

func probeGuest(ctx context.Context, c config, host string) error {
	alias, err := sshHostKeyAlias(ctx, c)
	if err != nil {
		return err
	}
	command := sshExecCommand(c, host, alias, "command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1")
	cmd := exec.CommandContext(ctx, command.Path, command.Args[1:]...)
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return fmt.Errorf("SSH/Docker probe: %w", ctx.Err())
	}
	if err != nil {
		if strings.Contains(string(out), "REMOTE HOST IDENTIFICATION HAS CHANGED") || strings.Contains(string(out), "Host key verification failed") {
			return fmt.Errorf("%w: %s", errHostKeyVerification, strings.TrimSpace(string(out)))
		}
		return fmt.Errorf("SSH/Docker probe: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
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
	progress("Waiting for virtual machine to become " + desired)
	deadline := time.Now().Add(90 * time.Second)
	lastState := ""
	for time.Now().Before(deadline) {
		status, err := readVMStatus(c)
		if err == nil {
			state := statusForVM(status, false)
			if status.Phase != lastState {
				progress("Virtual machine state: " + status.Phase)
				lastState = status.Phase
			}
			if state == desired {
				return nil
			}
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("timed out waiting for VM %s to become %s", c.VMName, desired)
}
func sshHost(c config) (string, error) {
	return sshHostContext(context.Background(), c)
}

func sshHostContext(ctx context.Context, c config) (string, error) {
	if c.SSHHost != "" {
		return c.SSHHost, nil
	}
	out, err := kubectlInputContext(ctx, c, nil, "get", "vmi", c.VMName, "-n", c.Namespace, "-o", "json")
	if err != nil {
		return "", err
	}
	var p struct {
		Status struct {
			Interfaces []struct {
				Name string `json:"name"`
				IP   string `json:"ipAddress"`
			} `json:"interfaces"`
		} `json:"status"`
	}
	if err = json.Unmarshal(out, &p); err != nil {
		return "", err
	}
	for _, i := range p.Status.Interfaces {
		if i.Name == "default" && i.IP != "" {
			return i.IP, nil
		}
	}
	return "", errors.New("VM has no IP address on its default network yet")
}
func sshCommand(c config, command string) error {
	host, err := sshHost(c)
	if err != nil {
		return err
	}
	return sshRun(c, host, command)
}
func sshRun(c config, host, command string) error {
	alias, err := sshHostKeyAlias(context.Background(), c)
	if err != nil {
		return err
	}
	x := sshExecCommand(c, host, alias, command)
	x.Stdin, x.Stdout, x.Stderr = os.Stdin, os.Stdout, os.Stderr
	return x.Run()
}
func sshHostKeyAlias(ctx context.Context, c config) (string, error) {
	out, err := kubectlInputContext(ctx, c, nil, "get", "virtualmachine", c.VMName, "-n", c.Namespace, "-o", "jsonpath={.metadata.uid}")
	if err != nil {
		return "", fmt.Errorf("resolve VM identity for SSH: %w", err)
	}
	uid := strings.TrimSpace(string(out))
	if uid == "" || strings.IndexFunc(uid, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-')
	}) >= 0 {
		return "", errors.New("VM has no valid UID for SSH host key verification")
	}
	return "devsy-harvester-" + uid, nil
}

func sshExecCommand(c config, host, hostKeyAlias, command string) *exec.Cmd {
	args := []string{"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new", "-o", "HostKeyAlias=" + hostKeyAlias, "-o", "CheckHostIP=no", "-p", c.SSHPort}
	if c.SSHFlags != "" {
		args = append(args, strings.Fields(c.SSHFlags)...)
	}
	args = append(args, c.SSHUser+"@"+host, command)
	// This is Devsy's long-lived transport, not a bounded control-plane call.
	return exec.Command("ssh", args...)
}

func deleteMachine(c config) error {
	progress("Deleting virtual machine; waiting for VM and dependent resources")
	if _, err := kubectl(c, "delete", "virtualmachine", c.VMName, "-n", c.Namespace, "--ignore-not-found", "--cascade=foreground", "--wait=true"); err != nil {
		return err
	}
	progress("Virtual machine deletion confirmed; deleting disk claims")
	var failures []error
	for _, suffix := range []string{"-rootdisk", "-image", "-blankdisk"} {
		name := c.VMName + suffix
		if _, err := kubectl(c, "delete", "pvc", name, "-n", c.Namespace, "--ignore-not-found", "--wait=true"); err != nil {
			failures = append(failures, fmt.Errorf("delete disk claim %s: %w", name, err))
		}
	}
	if err := errors.Join(failures...); err != nil {
		return err
	}
	progress("VM and PVC deletion confirmed")
	return nil
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
		progress("Resolving cloud-init configuration")
		_, err = resolveUserData(c)
		if err == nil {
			err = checkNetwork(c)
		}
		if err == nil {
			progress("Checking Harvester API connectivity")
			_, err = kubectl(c, "version", "--request-timeout=10s")
		}
		if err == nil {
			progress("Provider initialized")
		}
	case "create":
		err = apply(c)
	case "delete":
		err = deleteMachine(c)
	case "start":
		progress("Requesting virtual machine start")
		err = requestVMStart(c)
		if err == nil {
			err = waitForVM(c, "Running")
		}
	case "stop":
		progress("Requesting virtual machine shutdown")
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
