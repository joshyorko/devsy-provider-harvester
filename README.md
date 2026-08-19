# Devsy Harvester Provider

`devsy-provider-harvester` provisions Devsy machine providers as Harvester
VirtualMachine resources and connects to the resulting Linux VM over SSH.

## Image selection

The VM root disk is selected with `HARVESTER_IMAGE` and
`HARVESTER_IMAGE_NAMESPACE`. This follows Harvester's image-backed VM model
used by the official Terraform provider. The image can be a cloud image,
ISO-backed image, or another image type supported by the Harvester cluster;
the guest OS must provide SSH and Docker for the initial `driver: docker`
configuration.

## Status

This is an initial provider skeleton. It uses `kubectl` plus the Harvester
KubeVirt API, keeping provisioning separate from Devsy's workspace driver.
No separate KubeVirt driver is required for this first path.

## Build

```sh
go build -o harvester-provider ./cmd/harvester-provider
```

The binary must be available on `PATH` when Devsy runs the provider commands.
