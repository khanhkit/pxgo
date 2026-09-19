package config

import "testing"

func TestTOBSDOC013ParseDoctorAction(t *testing.T) {
	cfg, err := ParseArgs([]string{"--doctor"})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Doctor {
		t.Fatal("--doctor did not set Doctor action")
	}
}
