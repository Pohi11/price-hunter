package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("PH_ENV", "local")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTPAddr != ":8088" || c.WorkerConcurrency != 5 || c.CheckTimeout != 25*time.Second || !c.AllowGeneric {
		t.Fatalf("defaults: %+v", c)
	}
}

func TestValidateRejectsUnsafeCombinations(t *testing.T) {
	tests := map[string]map[string]string{
		"localhost outside local":   {"PH_ENV": "prod", "PH_ALLOW_LOCALHOST": "true"},
		"endpoint override in prod": {"PH_ENV": "prod", "PH_DYNAMODB_ENDPOINT": "http://localhost:8000"},
		"ses without sender":        {"PH_ENV": "dev", "PH_EMAIL_MODE": "ses"},
		"bad env":                   {"PH_ENV": "staging"},
		"bad bool":                  {"PH_ALLOW_GENERIC": "maybe"},
		"bad duration":              {"PH_CHECK_TIMEOUT": "soon"},
		"concurrency out of range":  {"PH_WORKER_CONCURRENCY": "500"},
	}
	for name, env := range tests {
		t.Run(name, func(t *testing.T) {
			for k, v := range env {
				t.Setenv(k, v)
			}
			if _, err := Load(); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestValidateAllowsLocalDemoStore(t *testing.T) {
	t.Setenv("PH_ENV", "local")
	t.Setenv("PH_ALLOW_LOCALHOST", "true")
	t.Setenv("PH_CORS_ORIGINS", "http://localhost:5173, http://127.0.0.1:5173")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(c.CORSOrigins, "|") != "http://localhost:5173|http://127.0.0.1:5173" {
		t.Fatalf("origins: %v", c.CORSOrigins)
	}
}
