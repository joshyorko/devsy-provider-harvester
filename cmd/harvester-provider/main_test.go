package main

import "testing"

func TestBuildVMManifestUsesHarvesterImagePVCAndRunStrategy(t *testing.T) {
	c := config{Namespace: "default", VMName: "machine-1", Image: "harvester-public/ubuntu", CPU: "4", Memory: "8Gi", Disk: "40Gi"}
	manifest, err := buildVMManifest(c)
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
