package main

import (
	"os/exec"
	"testing"
)

func TestSilhouette(t *testing.T) {
	cmd := exec.Command("uv", "run", "--with", "trimesh", "--with", "pillow", "--with", "numpy",
		"python3", "tests/test_silhouette.py")
	output, err := cmd.CombinedOutput()
	t.Log(string(output))
	if err != nil {
		t.Fatalf("silhouette test failed: %v", err)
	}
}
