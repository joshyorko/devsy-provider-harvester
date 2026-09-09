package main

import (
	"errors"
	"testing"
)

func TestBuildVMManifestUsesHarvesterImagePVCAndRunStrategy(t *testing.T) {
	c := config{Namespace: "default", VMName: "machine-1", Image: "harvester-public/ubuntu", CPU: "4", Memory: "8Gi", Disk: "40Gi"}
	manifest, err := buildVMManifest(c, imageInfo{Namespace: "harvester-public", Name: "ubuntu", StorageClass: "longhorn-image-harvester-public-ubuntu", Format: "raw"})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Spec.Running != nil {
		t.Fatal("manifest must not set deprecated running with runStrategy")
	}
	if manifest.Spec.Template.Spec.Volumes[0].PersistentVolumeClaim == nil {
		t.Fatal("manifest must use a PVC-backed root disk")
	}
	if manifest.Spec.Template.Spec.Volumes[0].PersistentVolumeClaim.ClaimName == "" {
		t.Fatal("root disk PVC claim name is required")
	}
}

func TestBuildVMManifestSetsGuestMemory(t *testing.T) {
	for _, memory := range []string{"2Gi", "8Gi", "512Mi"} {
		c := config{Namespace: "default", VMName: "machine-1", CPU: "2", Memory: memory, Disk: "12Gi"}
		manifest, err := buildVMManifest(c, imageInfo{Format: "qcow2"})
		if err != nil {
			t.Fatal(err)
		}
		guest, ok := manifest.Spec.Template.Spec.Domain["memory"].(map[string]string)
		if !ok || guest["guest"] != memory {
			t.Fatalf("Harvester admission requires domain.memory.guest=%q, got %#v", memory, manifest.Spec.Template.Spec.Domain["memory"])
		}
	}
}

func TestBuildPVCManifestUsesImageStorageClassAndBlockMode(t *testing.T) {
	c := config{Namespace: "default", VMName: "machine-1", Disk: "40Gi"}
	pvc, err := buildPVCManifest(c, imageInfo{Namespace: "harvester-public", Name: "ubuntu", StorageClass: "longhorn-image-harvester-public-ubuntu"})
	if err != nil {
		t.Fatal(err)
	}
	if pvc.Spec["storageClassName"] != "longhorn-image-harvester-public-ubuntu" {
		t.Fatalf("storage class: %#v", pvc.Spec["storageClassName"])
	}
	if pvc.Spec["volumeMode"] != "Block" {
		t.Fatalf("volume mode: %#v", pvc.Spec["volumeMode"])
	}
}

func TestBuildVMManifestUsesCDROMForISOImage(t *testing.T) {
	c := config{Namespace: "default", VMName: "machine-1", CPU: "4", Memory: "8Gi", Disk: "40Gi"}
	manifest, err := buildVMManifest(c, imageInfo{Namespace: "default", Name: "installer", StorageClass: "longhorn-image-default-installer", Format: "iso"})
	if err != nil {
		t.Fatal(err)
	}
	disks := manifest.Spec.Template.Spec.Domain["devices"].(map[string]any)["disks"].([]map[string]any)
	if disks[0]["cdrom"] == nil {
		t.Fatal("ISO image must be attached as a CD-ROM")
	}
}

func TestParseImageReference(t *testing.T) {
	got, err := parseImageReference("custom/image", "harvester-public")
	if err != nil {
		t.Fatal(err)
	}
	if got.Namespace != "custom" || got.Name != "image" {
		t.Fatalf("got %#v", got)
	}
}

func TestNotFoundErrorDetection(t *testing.T) {
	if !isNotFoundError(errors.New("kubectl: virtualmachine not found: exit status 1")) {
		t.Fatal("expected not-found error")
	}
	if isNotFoundError(errors.New("kubectl: forbidden: exit status 1")) {
		t.Fatal("forbidden must not be treated as not found")
	}
}

func TestStatusForVMUsesPhaseAndNotFound(t *testing.T) {
	if got := statusForVM(nil, true); got != "NotFound" {
		t.Fatalf("got %q", got)
	}
	if got := statusForVM(&vmStatus{Ready: true}, false); got != "Running" {
		t.Fatalf("got %q", got)
	}
	if got := statusForVM(&vmStatus{Ready: false, Phase: "Scheduling"}, false); got != "Busy" {
		t.Fatalf("got %q", got)
	}
	if got := statusForVM(&vmStatus{Ready: false, Phase: "Stopped"}, false); got != "Stopped" {
		t.Fatalf("got %q", got)
	}
}

func TestValidateConfigRequiresImageAndSSHBootstrap(t *testing.T) {
	c := config{Image: "ubuntu"}
	if err := validateConfig(c, "create"); err == nil {
		t.Fatal("expected missing SSH bootstrap configuration to fail")
	}
}
