package store

import "testing"

func TestPostgresDefaultsToVerifiedTLS(t *testing.T) {
	t.Setenv("PGSSLMODE", "prefer")
	for _, dsn := range []string{
		"postgres://owner:secret@db.example/ether",
		"postgresql://owner:secret@db.example/ether?application_name=ether",
		"postgres://owner@db.example/ether?sslmode=",
		"host=db.example user=owner dbname=ether",
	} {
		cfg, err := parsePostgresConfig(dsn)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.TLSConfig == nil || cfg.TLSConfig.InsecureSkipVerify || cfg.TLSConfig.ServerName != "db.example" {
			t.Fatalf("default must verify the database hostname: %+v", cfg.TLSConfig)
		}
		for _, fallback := range cfg.Fallbacks {
			if fallback.TLSConfig == nil || fallback.TLSConfig.InsecureSkipVerify {
				t.Fatal("insecure fallback enabled")
			}
		}
	}
}

func TestPostgresPreservesExplicitOwnerTLSMode(t *testing.T) {
	for _, dsn := range []string{
		"postgres://owner@db.example/ether?sslmode=disable",
		"host=db.example user=owner sslmode=disable",
	} {
		cfg, err := parsePostgresConfig(dsn)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.TLSConfig != nil {
			t.Fatal("explicit disable was not preserved")
		}
	}
}
