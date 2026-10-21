package main

import (
	"crypto/ed25519"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/braedonsaunders/ether/internal/updater"
)

// cmdRenew authenticates an existing publisher manifest before extending its
// freshness. Detached signatures keep old clients compatible: they authenticate
// the exact JSON bytes, including fields their legacy structs do not know.
func cmdRenew(args []string) error {
	fs := flag.NewFlagSet("renew", flag.ContinueOnError)
	in := fs.String("in", "", "existing signed manifest")
	out := fs.String("out", "", "output manifest (required, must differ from input)")
	pubSpec := fs.String("pub", "", "public key or ETHER_UPDATE_PUBLIC_KEY")
	keySpec := fs.String("key", "", "private key or ETHER_UPDATE_PRIVATE_KEY")
	version := fs.String("version", "", "expected published release version (required)")
	channel := fs.String("channel", "", "expected channel (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *in == "" || *out == "" || *out == *in || *version == "" || *channel == "" {
		return fmt.Errorf("renew requires distinct -in/-out, -version and -channel")
	}
	if *pubSpec == "" {
		*pubSpec = os.Getenv("ETHER_UPDATE_PUBLIC_KEY")
	}
	pub, err := updater.PublicKeyFromB64(*pubSpec)
	if err != nil {
		return err
	}
	m, raw, err := readManifest(*in)
	if err != nil {
		return err
	}
	if err := m.Validate(); err != nil {
		return err
	}
	if m.Signature != "" {
		if err := m.Verify(pub); err != nil {
			return err
		}
	} else {
		sig, err := os.ReadFile(*in + ".sig")
		if err != nil {
			return err
		}
		if err := updater.VerifyDetached(pub, raw, string(sig)); err != nil {
			return err
		}
	}
	if m.Version != *version || m.Channel != *channel {
		return fmt.Errorf("manifest does not match expected published release/channel")
	}
	priv, err := loadPrivateKey(*keySpec)
	if err != nil {
		return err
	}
	if !pub.Equal(priv.Public().(ed25519.PublicKey)) {
		return fmt.Errorf("release key pair does not match")
	}
	m.Signature = ""
	m.RenewFreshness(time.Now())
	raw, err = json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	sig, err := updater.SignDetached(priv, raw)
	if err != nil {
		return err
	}
	if err := os.WriteFile(*out, raw, 0o644); err != nil {
		return err
	}
	return os.WriteFile(*out+".sig", []byte(sig+"\n"), 0o644)
}
