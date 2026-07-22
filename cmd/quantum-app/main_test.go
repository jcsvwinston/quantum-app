package main

import (
	"strings"
	"testing"
)

// TestValidateDeploymentSecret is the SEC-1 gate at the unit level: a required
// deployment secret is rejected when it is unset, empty, or still set to a
// public example value from the repository, and accepted only for a real,
// operator-provided value. mustEnv turns a non-nil error here into a
// log.Fatalf at startup — so this test covers the fail-closed decision
// without exiting the process.
func TestValidateDeploymentSecret(t *testing.T) {
	const key = "WAREHOUSE_OUTBOX_SECRET"
	cases := []struct {
		name    string
		value   string
		present bool
		wantErr string // substring the error must contain; "" means expect nil
	}{
		{"unset is rejected", "", false, "must be set"},
		{"empty is rejected", "", true, "must be set"},
		{"dev outbox secret example value is rejected", "dev-outbox-secret", true, "example"},
		{"dev outbox token example value is rejected", "dev-outbox-token", true, "example"},
		{"warehouse-ops example value is rejected", "warehouse-ops", true, "example"},
		{"a real operator-provided value is accepted", "s3cr3t-from-the-vault", true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateDeploymentSecret(key, tc.value, tc.present)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validateDeploymentSecret(%q, present=%v) = %v, want nil", tc.value, tc.present, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateDeploymentSecret(%q, present=%v) = nil, want error containing %q", tc.value, tc.present, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("validateDeploymentSecret(%q): error %q does not contain %q", tc.value, err.Error(), tc.wantErr)
			}
		})
	}
}
