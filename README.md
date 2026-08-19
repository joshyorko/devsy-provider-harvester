# Devsy Harvester Provider

`devsy-provider-harvester` provisions Devsy machine providers as Harvester
VirtualMachine resources and connects to the resulting Linux VM over SSH.
Devsy downloads the provider helper from the tagged GitHub release described by
`provider.yaml`.

## Image selection

The VM root disk is selected with `HARVESTER_IMAGE` and
`HARVESTER_IMAGE_NAMESPACE`. The provider creates a Harvester image-backed PVC
and attaches it to the KubeVirt VM. The image can be a cloud image, ISO-backed
image, or another image type supported by the Harvester cluster. Supply
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

The binary must be available on `PATH` when Devsy runs the provider commands.
