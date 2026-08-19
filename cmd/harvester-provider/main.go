package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

type config struct {
	Kubeconfig string
	Context    string
	Namespace  string
	VMName     string
	Image      string
	ImageNS    string
	CPU        string
	Memory     string
	Disk       string
	SSHHost    string
	SSHUser    string
	SSHPort    string
	SSHFlags   string
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func load() config {
	return config{Kubeconfig: env("KUBECONFIG", ""), Context: env("HARVESTER_CONTEXT", ""), Namespace: env("HARVESTER_NAMESPACE", "default"), VMName: env("HARVESTER_VM_NAME", env("MACHINE_ID", "devsy-workspace")), Image: os.Getenv("HARVESTER_IMAGE"), ImageNS: env("HARVESTER_IMAGE_NAMESPACE", "harvester-public"), CPU: env("HARVESTER_CPU", "4"), Memory: env("HARVESTER_MEMORY", "8Gi"), Disk: env("HARVESTER_DISK", "40Gi"), SSHHost: os.Getenv("HARVESTER_SSH_HOST"), SSHUser: env("HARVESTER_SSH_USER", "ubuntu"), SSHPort: env("HARVESTER_SSH_PORT", "22"), SSHFlags: os.Getenv("HARVESTER_SSH_FLAGS")}
}

func kubectl(c config, args ...string) ([]byte, error) {
	if c.Kubeconfig != "" {
		args = append([]string{"--kubeconfig", c.Kubeconfig}, args...)
	}
	if c.Context != "" {
		args = append([]string{"--context", c.Context}, args...)
	}
	cmd := exec.Command("kubectl", args...)
	cmd.Stderr = os.Stderr
	return cmd.Output()
}

func vmJSON(c config, running *bool) []byte {
	run := "false"
	if running != nil && *running {
		run = "true"
	}
	return []byte(fmt.Sprintf(`{"apiVersion":"kubevirt.io/v1","kind":"VirtualMachine","metadata":{"name":%q,"namespace":%q,"labels":{"devsy.sh/provider":"harvester","devsy.sh/machine-id":%q}},"spec":{"runStrategy":"RerunOnFailure","template":{"metadata":{"labels":{"devsy.sh/provider":"harvester"}},"spec":{"domain":{"cpu":{"cores":%s},"resources":{"requests":{"memory":%q}},"devices":{"disks":[{"name":"rootdisk","disk":{"bus":"virtio"}}],"interfaces":[{"name":"default","bridge":{}}]}},"networks":[{"name":"default","pod":{}}],"volumes":[{"name":"rootdisk","harvesterDisk":{"image":%q,"imageNamespace":%q,"size":%q}}]}},"running":%s}}`, c.VMName, c.Namespace, c.VMName, c.CPU, c.Memory, c.Image, c.ImageNS, c.Disk, run))
}

func apply(c config, running bool) error {
	if c.Image == "" {
		return errors.New("HARVESTER_IMAGE is required (Harvester image name or namespace/name)")
	}
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	if c.Kubeconfig != "" {
		cmd.Args = append([]string{"kubectl", "--kubeconfig", c.Kubeconfig}, cmd.Args[1:]...)
	}
	if c.Context != "" {
		cmd.Args = append([]string{"kubectl", "--context", c.Context}, cmd.Args[1:]...)
	}
	cmd.Stdin = strings.NewReader(string(vmJSON(c, &running)))
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func sshCommand(c config, command string) error {
	if c.SSHHost == "" {
		return errors.New("HARVESTER_SSH_HOST is required")
	}
	args := []string{"-p", c.SSHPort}
	if c.SSHFlags != "" {
		args = append(args, strings.Fields(c.SSHFlags)...)
	}
	args = append(args, c.SSHUser+"@"+c.SSHHost, command)
	x := exec.Command("ssh", args...)
	x.Stdin, x.Stdout, x.Stderr = os.Stdin, os.Stdout, os.Stderr
	return x.Run()
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = ctx
	c := load()
	command := "command"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	switch command {
	case "init":
		if _, err := kubectl(c, "version", "--request-timeout=10s"); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "create":
		if err := apply(c, true); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "delete":
		if _, err := kubectl(c, "delete", "virtualmachine", c.VMName, "-n", c.Namespace, "--ignore-not-found"); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "start":
		if _, err := kubectl(c, "patch", "virtualmachine", c.VMName, "-n", c.Namespace, "--type=merge", "-p", `{"spec":{"runStrategy":"RerunOnFailure"}}`); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "stop":
		if _, err := kubectl(c, "patch", "virtualmachine", c.VMName, "-n", c.Namespace, "--type=merge", "-p", `{"spec":{"runStrategy":"Halted"}}`); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "status":
		out, err := kubectl(c, "get", "virtualmachine", c.VMName, "-n", c.Namespace, "-o", "json")
		if err != nil {
			fmt.Print("Stopped")
			return
		}
		var v struct {
			Spec struct {
				Running *bool `json:"running"`
			} `json:"spec"`
		}
		_ = json.Unmarshal(out, &v)
		if v.Spec.Running != nil && *v.Spec.Running {
			fmt.Print("Running")
		} else {
			fmt.Print("Stopped")
		}
	case "command":
		fs := flag.NewFlagSet("command", flag.ExitOnError)
		cmd := fs.String("command", "", "")
		_ = fs.Parse(os.Args[2:])
		if err := sshCommand(c, *cmd); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	default:
		fmt.Fprintln(os.Stderr, "usage: harvester-provider {init|create|delete|start|stop|status|command}")
		os.Exit(2)
	}
}
