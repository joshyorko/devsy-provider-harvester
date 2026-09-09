# Devsy Harvester Provider

`devsy-provider-harvester` provisions Devsy machine providers as Harvester
VirtualMachine resources and connects to the resulting Linux VM over SSH.
Devsy downloads the provider helper from the version-pinned GitHub release described by
`provider.yaml`.

## Install or update

Use the canonical GitHub source (without an `https://` prefix) so Devsy can
discover releases and consume the checksummed release manifest:

```sh
devsy provider add github.com/joshyorko/devsy-provider-harvester
# Preserve an existing registration and its saved options:
devsy provider set-source harvester github.com/joshyorko/devsy-provider-harvester@v0.1.7 --use=false
devsy provider init harvester
devsy provider versions harvester --json --no-cache
```

The release asset `provider.yaml` includes per-platform SHA-256 checksums;
the repository manifest is the release template, not the recommended install URL.

## Desktop and Windows prerequisites

Windows amd64, Linux amd64/arm64, and macOS amd64/arm64 helpers are packaged.
Devsy automatically downloads a pinned, checksummed `kubectl` for your platform;
no kubectl PATH setup is required. Install OpenSSH on the client. To override
the managed kubectl, set `HARVESTER_KUBECTL_PATH` to an absolute executable path.
Use a kubeconfig path valid on that client; kubeconfigs and private SSH keys are
not distributed with the provider. Windows uses the same Linux SSH/Docker guest.

## Image selection

The VM root disk is selected with `HARVESTER_IMAGE` and
`HARVESTER_IMAGE_NAMESPACE`. The provider resolves the image's Harvester
storage class before creating an image-backed PVC and attaching it to the
KubeVirt VM. Set `HARVESTER_IMAGE_TYPE` to `raw`, `qcow2`, or `iso`; ISO images
are attached as a CD-ROM alongside a separate writable root disk. Supply
`HARVESTER_SSH_PUBLIC_KEY` or complete `HARVESTER_USER_DATA` so cloud-init can
bootstrap the guest; the guest image must provide SSH and Docker for the
initial `driver: docker` configuration.

### Cloud-init CLI quoting and network access

`devsy machine create --provider-option` parses each argument as CSV. Wrap a
complete `HARVESTER_USER_DATA=...` value in CSV quotes (double embedded quotes),
including all newlines, rather than passing unquoted multiline text. Workspace
`up --provider-option` uses an array flag and does not need this CSV layer.

The default VM uses the pod network. Your client must route to its IP, or use a
scoped SSH proxy. For example, an executable wrapper around
`virtctl --kubeconfig ... --context ... port-forward --stdio=true vm/NAME/NAMESPACE 22`
can be supplied with `HARVESTER_SSH_HOST=127.0.0.1` and
`HARVESTER_SSH_FLAGS=-o ProxyCommand=/absolute/path/to/wrapper`. This uses the
authenticated Kubernetes API without exposing a node port or changing cluster
network policy. SSH flags currently accept simple whitespace-separated tokens;
put commands containing spaces in a wrapper.

## Status

The provider uses `kubectl` plus the Harvester KubeVirt API, keeping
provisioning separate from Devsy's workspace driver. It discovers the VM IP
from the VMI when `HARVESTER_SSH_HOST` is not supplied. No separate KubeVirt
driver is required for this path.

## Build

```sh
go build -o harvester-provider ./cmd/harvester-provider
```

For local development, build the binary and place it on `PATH`; normal Devsy
installs download the matching helper from the GitHub release.
