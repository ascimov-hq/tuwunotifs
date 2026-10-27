package main

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Listen          string   `yaml:"listen"`
	ProjectID       string   `yaml:"fcm_project_id"`
	CredentialsFile string   `yaml:"credentials_file"`
	AppIDs          []string `yaml:"app_ids"`
	Alert           string   `yaml:"alert"`
}

func defaultConfig() Config {
	return Config{
		Listen:          "0.0.0.0:8096",
		CredentialsFile: "/etc/tuwunotifs/fcm.json",
		Alert:           "New message",
	}
}

func loadConfig(path string) (Config, error) {
	c := defaultConfig()
	raw, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return c, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(c.AppIDs) == 0 {
		return c, fmt.Errorf("app_ids is required")
	}
	if c.Alert == "" {
		return c, fmt.Errorf("alert must not be empty")
	}
	return c, nil
}
