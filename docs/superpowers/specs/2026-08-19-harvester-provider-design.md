# Harvester Provider Design

The provider is a Devsy machine provider named `devsy-provider-harvester`.
It creates and manages Harvester `VirtualMachine` resources through the
cluster's Kubernetes API, then executes Devsy commands in the VM over SSH.

Image selection is explicit through `HARVESTER_IMAGE` and
`HARVESTER_IMAGE_NAMESPACE`, matching Harvester's image-backed VM model. The
first implementation keeps Devsy's built-in Docker agent driver inside the
guest. A custom KubeVirt driver is deferred because Devsy drivers run workspace
containers, while this provider owns VM lifecycle.
