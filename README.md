# Devsy Harvester Provider

`devsy-provider-harvester` provisions Devsy machine providers as Harvester
VirtualMachine resources and connects to the resulting Linux VM over SSH.
Devsy downloads the provider helper from the version-pinned GitHub release described by
`provider.yaml`.

## Image selection

The VM root disk is selected with `HARVESTER_IMAGE` and
`HARVESTER_IMAGE_NAMESPACE`. The provider resolves the image's Harvester
storage class before creating an image-backed PVC and attaching it to the
KubeVirt VM. Set `HARVESTER_IMAGE_TYPE` to `raw`, `qcow2`, or `iso`; ISO images
are attached as a CD-ROM alongside a separate writable root disk. Supply
`HARVESTER_SSH_PUBLIC_KEY` or complete `HARVESTER_USER_DATA` so cloud-init can
bootstrap the guest; the guest image must provide SSH and Docker for the
initial `driver: docker` configuration.

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
