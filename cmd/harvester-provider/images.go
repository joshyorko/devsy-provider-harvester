package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"text/tabwriter"
	"unicode"
)

type harvesterImage struct {
	Metadata metadata `json:"metadata"`
	Spec     struct {
		DisplayName string `json:"displayName"`
		Backend     string `json:"backend"`
		URL         string `json:"url"`
	} `json:"spec"`
	Status struct {
		StorageClassName string `json:"storageClassName"`
		Conditions       []struct {
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"conditions"`
	} `json:"status"`
}

type imageCatalogEntry struct {
	DisplayName string `json:"displayName"`
	Namespace   string `json:"namespace"`
	Name        string `json:"name"`
	Reference   string `json:"reference"`
	Ready       bool   `json:"ready"`
	Status      string `json:"status"`
}

func imageReadiness(image harvesterImage) string {
	if image.Metadata.DeletionTimestamp != "" {
		return "deleting"
	}
	if image.Status.StorageClassName == "" {
		return "storage class unavailable"
	}
	imported := false
	for _, condition := range image.Status.Conditions {
		if condition.Type == "Imported" && condition.Status == "True" {
			imported = true
		}
		if condition.Type == "RetryLimitExceeded" && condition.Status == "True" {
			return "import retry limit exceeded"
		}
	}
	if !imported {
		return "import incomplete"
	}
	return "ready"
}

func listImages(c config, namespace string) ([]harvesterImage, error) {
	out, err := kubectl(c, "get", "virtualmachineimages", "-n", namespace, "-o", "json")
	if err != nil {
		return nil, err
	}
	var list struct {
		Items []harvesterImage `json:"items"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return nil, fmt.Errorf("decode Harvester image catalog: %w", err)
	}
	if list.Items == nil {
		return nil, errors.New("image catalog response is missing its items list")
	}
	for _, image := range list.Items {
		if image.Metadata.Name == "" || image.Metadata.Namespace != namespace {
			return nil, errors.New("image catalog returned a missing name or unexpected namespace")
		}
	}
	sort.Slice(list.Items, func(i, j int) bool {
		a, b := list.Items[i], list.Items[j]
		if strings.ToLower(imageDisplayName(a)) != strings.ToLower(imageDisplayName(b)) {
			return strings.ToLower(imageDisplayName(a)) < strings.ToLower(imageDisplayName(b))
		}
		return a.Metadata.Name < b.Metadata.Name
	})
	return list.Items, nil
}

func imageDisplayName(image harvesterImage) string {
	if image.Spec.DisplayName != "" {
		return image.Spec.DisplayName
	}
	return image.Metadata.Name
}

func imageCatalog(c config) ([]imageCatalogEntry, error) {
	images, err := listImages(c, c.ImageNS)
	if err != nil {
		return nil, err
	}
	entries := make([]imageCatalogEntry, 0, len(images))
	for _, image := range images {
		status := imageReadiness(image)
		entries = append(entries, imageCatalogEntry{imageDisplayName(image), image.Metadata.Namespace, image.Metadata.Name,
			image.Metadata.Namespace + "/" + image.Metadata.Name, status == "ready", status})
	}
	return entries, nil
}

func printImageOptions(c config, out io.Writer) error {
	entries, err := imageCatalog(c)
	description := "Choose a ready Harvester image by display name. This overrides the manual image setting; choose Use manual image setting to clear it."
	manualLabel := "Use manual image setting"
	if err != nil {
		// Optional discovery must not break existing get-only credentials.
		// Keep the error visible in the form; the images command reports details.
		description = "Image catalog unavailable. Check kubeconfig, context, image namespace and image-list permission. The saved selection is preserved; you can still use a manual image reference. Run the helper's images command for details."
		manualLabel += " (catalog unavailable)"
	}
	type choice struct {
		Value       string `json:"value"`
		DisplayName string `json:"displayName"`
	}
	// An explicit empty choice prevents Devsy from auto-selecting a sole image.
	choices := []choice{{"", manualLabel}}
	pin := env("HARVESTER_IMAGE_PIN", os.Getenv("HARVESTER_IMAGE_CHOICE"))
	found := pin == ""
	for _, entry := range entries {
		if !entry.Ready && entry.Reference != pin {
			continue
		}
		label := entry.DisplayName + " (" + entry.Reference + ")"
		if !entry.Ready {
			label += " — " + entry.Status
		}
		choices = append(choices, choice{entry.Reference, label})
		found = found || entry.Reference == pin
	}
	if !found {
		choices = append(choices, choice{pin, "Saved image: " + pin + " (not in current catalog)"})
	}
	return json.NewEncoder(out).Encode(map[string]any{"options": map[string]any{
		"HARVESTER_IMAGE_CHOICE": map[string]any{
			"description": description,
			"required":    false, "default": pin, "enum": choices,
		},
	}})
}

func printImages(c config, asJSON bool, out io.Writer) error {
	entries, err := imageCatalog(c)
	if err != nil {
		return err
	}
	if asJSON {
		return json.NewEncoder(out).Encode(entries)
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "DISPLAY NAME\tREFERENCE\tSTATUS")
	for _, entry := range entries {
		name := strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return ' '
			}
			return r
		}, entry.DisplayName)
		fmt.Fprintf(w, "%s\t%s\t%s\n", name, entry.Reference, entry.Status)
	}
	return w.Flush()
}

var resourceImageName = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`)

func resolveImage(c config) (imageInfo, error) {
	ref, err := parseImageReference(c.Image, c.ImageNS)
	if err != nil {
		return imageInfo{}, err
	}
	var selected *harvesterImage
	if resourceImageName.MatchString(ref.Name) {
		out, err := kubectl(c, "get", "virtualmachineimage", ref.Name, "-n", ref.Namespace, "-o", "json", "--ignore-not-found")
		if err != nil {
			return imageInfo{}, err
		}
		if len(bytes.TrimSpace(out)) != 0 {
			var image harvesterImage
			if err := json.Unmarshal(out, &image); err != nil {
				return imageInfo{}, fmt.Errorf("decode Harvester image: %w", err)
			}
			if image.Metadata.Name != ref.Name || image.Metadata.Namespace != ref.Namespace {
				return imageInfo{}, errors.New("image lookup returned an unexpected resource identity")
			}
			selected = &image
		}
	}
	// Namespaced references and Harvester-generated IDs are stable identifiers.
	// Never reinterpret a deleted ID as another image's display name.
	if selected == nil && (strings.Contains(c.Image, "/") || strings.HasPrefix(ref.Name, "image-")) {
		return imageInfo{}, fmt.Errorf("Harvester image %s/%s not found", ref.Namespace, ref.Name)
	}
	if selected == nil {
		images, err := listImages(c, ref.Namespace)
		if err != nil {
			return imageInfo{}, err
		}
		var matches []string
		for i := range images {
			if images[i].Spec.DisplayName == c.Image {
				selected = &images[i]
				matches = append(matches, images[i].Metadata.Namespace+"/"+images[i].Metadata.Name)
			}
		}
		if len(matches) > 1 {
			return imageInfo{}, fmt.Errorf("image display name %q is ambiguous; use an exact reference: %s", c.Image, strings.Join(matches, ", "))
		}
	}
	if selected == nil {
		return imageInfo{}, fmt.Errorf("Harvester image %q not found in namespace %s; list images to choose a reference", c.Image, ref.Namespace)
	}
	if status := imageReadiness(*selected); status != "ready" {
		return imageInfo{}, fmt.Errorf("Harvester image %s/%s is not ready: %s", ref.Namespace, selected.Metadata.Name, status)
	}
	image := imageInfo{Namespace: ref.Namespace, Name: selected.Metadata.Name, StorageClass: selected.Status.StorageClassName, Format: c.ImageType}
	if selected.Spec.Backend == "cdi" && image.Format == "raw" && strings.HasSuffix(strings.ToLower(selected.Spec.URL), ".iso") {
		image.Format = "iso"
	}
	return image, nil
}
