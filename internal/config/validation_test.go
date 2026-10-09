package config_test

import (
	"errors"
	"strings"
	"testing"

	"gitlab.bodesitech.com/bodesi/confhub/internal/config"
)

func TestPublicationRejectsMalformedDeclaredFormats(t *testing.T) {
	for _, tc := range []struct{ format, valid, invalid string }{
		{"json", `{"port":8080}`, `{"port":}`},
		{"yaml", "port: 8080\n", "port: [\n"},
		{"toml", "port = 8080\n", "port = [\n"},
		{"xml", "<config><port>8080</port></config>", "<config><port></config>"},
		{"ini", "[server]\nport=8080\n", "[server\n"},
		{"properties", "port=8080\n", `port=\uXXXX`},
	} {
		t.Run(tc.format, func(t *testing.T) {
			if err := config.ValidateContent(tc.format, tc.valid); err != nil {
				t.Fatalf("valid input rejected: %v", err)
			}
			if err := config.ValidateContent(tc.format, tc.invalid); !errors.Is(err, config.ErrInvalid) {
				t.Fatalf("malformed input accepted: %v", err)
			}
		})
	}
	if err := config.ValidateContent("text", "anything { ["); err != nil {
		t.Fatal(err)
	}
	if err := config.ValidateContent("unknown", "x"); !errors.Is(err, config.ErrInvalid) {
		t.Fatal("unknown format accepted")
	}
	if err := config.ValidateContent("text", strings.Repeat("x", config.MaxContentBytes+1)); !errors.Is(err, config.ErrInvalid) {
		t.Fatal("oversize content accepted")
	}
}

func TestPropertiesValidationDoesNotResolveApplicationPlaceholders(t *testing.T) {
	if err := config.ValidateContent("properties", "port=${port}\n"); err != nil {
		t.Fatalf("syntax validation tried to resolve an application variable: %v", err)
	}
}
