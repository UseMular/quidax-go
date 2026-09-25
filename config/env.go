package config

import (
	"os"
	"strings"
)

func valueOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func secret(name string) string {
	if path := os.Getenv(name + "_FILE"); path != "" {
		value, err := os.ReadFile(path)
		if err != nil {
			panic("could not read " + name + "_FILE: " + err.Error())
		}
		return strings.TrimSpace(string(value))
	}
	return os.Getenv(name)
}

var (
	DATA_DB_URL      = valueOrDefault("DATA_DB_URL", "127.0.0.1:3306")
	DATA_DB_NAME     = valueOrDefault("DATA_DB_NAME", "quidax-go")
	DATA_DB_USER     = valueOrDefault("DATA_DB_USER", "quidax")
	DATA_DB_PASSWORD = secret("DATA_DB_PASSWORD")
	TX_DB_URL        = valueOrDefault("TX_DB_URL", "127.0.0.1:3000")
	PORT             = valueOrDefault("PORT", ":8080")
)
