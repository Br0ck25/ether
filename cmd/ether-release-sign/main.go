// Command ether-release-sign manages Ether's release-signing keys and
// signs update manifests. It is a CI/maintainer tool; it is not shipped in
// installers.
//
// Usage:
//
//	ether-release-sign keygen [-out key.txt]
//	    Generate an ed25519 key pair. Prints (or writes) the private key
//	    (keep secret; CI: ETHER_UPDATE_PRIVATE_KEY) and the public key
//	    (bake into releases via -ldflags -X updater.PublicKeyB64=...).
//
//	ether-release-sign sign -key <b64|file> -in manifest.json [-out signed.json] [-detached]
//	    Sign a manifest. Default embeds the signature; -detached writes
//	    <out>.sig instead and leaves the manifest bytes untouched.
//	    The key may also come from $ETHER_UPDATE_PRIVATE_KEY.
//
//	ether-release-sign verify -pub <b64> -in manifest.json [-sig manifest.json.sig]
//	    Verify an embedded or detached signature.
//
//	ether-release-sign manifest -version 1.2.0 -channel stable -artifact file:os:arch:kind:url [-artifact ...] [-notes URL] [-out manifest.json]
//	    Build an unsigned manifest from local artifact files (hashes and
//	    sizes are computed here, so CI cannot publish a wrong checksum).
package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/updater"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "keygen":
		err = cmdKeygen(os.Args[2:])
	case "sign":
		err = cmdSign(os.Args[2:])
	case "verify":
		err = cmdVerify(os.Args[2:])
	case "manifest":
		err = cmdManifest(os.Args[2:])
	case "renew":
		err = cmdRenew(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ether-release-sign:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: ether-release-sign <keygen|sign|verify|manifest|renew> [flags]  (see package docs)")
}

func cmdKeygen(args []string) error {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	out := fs.String("out", "", "write keys to this new file (0600; never overwritten) instead of stdout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	pub, priv, err := updater.GenerateKeyPair()
	if err != nil {
		return err
	}
	text := fmt.Sprintf("ETHER_UPDATE_PRIVATE_KEY=%s\nETHER_UPDATE_PUBLIC_KEY=%s\n",
		updater.EncodeKey(priv), updater.EncodeKey(pub))
	if *out == "" {
		fmt.Print(text)
		fmt.Fprintln(os.Stderr, "keep the private key secret; embed the public key with:")
		fmt.Fprintf(os.Stderr, "  -ldflags \"-X github.com/braedonsaunders/ether/internal/updater.PublicKeyB64=%s\"\n", updater.EncodeKey(pub))
		return nil
	}
	return writeNewSecret(*out, []byte(text))
}

// writeNewSecret creates path with owner-only permissions. It refuses to
// replace an existing file: overwriting would destroy the current release
// key, and os.WriteFile would keep a looser mode already on the file.
func writeNewSecret(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		_ = os.Remove(path)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

func loadPrivateKey(spec string) (ed25519.PrivateKey, error) {
	if spec == "" {
		spec = os.Getenv("ETHER_UPDATE_PRIVATE_KEY")
	}
	if spec == "" {
		return nil, fmt.Errorf("no key: pass -key or set ETHER_UPDATE_PRIVATE_KEY")
	}
	if _, err := os.Stat(spec); err == nil {
		b, err := os.ReadFile(spec)
		if err != nil {
			return nil, err
		}
		s := strings.TrimSpace(string(b))
		// Accept a keygen file ("ETHER_UPDATE_PRIVATE_KEY=...").
		for _, line := range strings.Split(s, "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(line), "ETHER_UPDATE_PRIVATE_KEY="); ok {
				s = v
				break
			}
		}
		spec = strings.TrimSpace(s)
	}
	return updater.PrivateKeyFromB64(spec)
}

func readManifest(path string) (*updater.Manifest, []byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var m updater.Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &m, b, nil
}

func cmdSign(args []string) error {
	fs := flag.NewFlagSet("sign", flag.ExitOnError)
	keySpec := fs.String("key", "", "base64 private key, or a file containing one")
	in := fs.String("in", "", "unsigned manifest JSON (required)")
	out := fs.String("out", "", "signed manifest path (default: overwrite -in)")
	detached := fs.Bool("detached", false, "write a detached <out>.sig; leave manifest unchanged")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *in == "" {
		return fmt.Errorf("-in is required")
	}
	priv, err := loadPrivateKey(*keySpec)
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
	if err := m.ValidateFreshness(time.Now()); err != nil {
		return err
	}
	outPath := *out
	if outPath == "" {
		outPath = *in
	}
	if *detached {
		sig, err := updater.SignDetached(priv, raw)
		if err != nil {
			return err
		}
		if outPath != *in {
			if err := os.WriteFile(outPath, raw, 0o644); err != nil {
				return err
			}
		}
		return os.WriteFile(outPath+".sig", []byte(sig+"\n"), 0o644)
	}
	if err := m.Sign(priv); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(outPath, append(b, '\n'), 0o644)
}

func cmdVerify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	pubSpec := fs.String("pub", "", "base64 public key (or $ETHER_UPDATE_PUBLIC_KEY)")
	in := fs.String("in", "", "manifest JSON (required)")
	sigPath := fs.String("sig", "", "detached signature file (default: embedded signature)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *in == "" {
		return fmt.Errorf("-in is required")
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
	if *sigPath != "" {
		sig, err := os.ReadFile(*sigPath)
		if err != nil {
			return err
		}
		if err := updater.VerifyDetached(pub, raw, string(sig)); err != nil {
			return err
		}
	} else if err := m.Verify(pub); err != nil {
		return err
	}
	if err := m.ValidateFreshness(time.Now()); err != nil {
		return err
	}
	fmt.Printf("ok: %s %s (%s)\n", m.Channel, m.Version, *in)
	return nil
}

type artifactFlags []string

func (a *artifactFlags) String() string { return strings.Join(*a, ",") }
func (a *artifactFlags) Set(v string) error {
	*a = append(*a, v)
	return nil
}

func cmdManifest(args []string) error {
	fs := flag.NewFlagSet("manifest", flag.ExitOnError)
	version := fs.String("version", "", "release version (required)")
	channel := fs.String("channel", "stable", "stable|beta")
	notesURL := fs.String("notes", "", "release notes URL")
	out := fs.String("out", "", "output path (default stdout)")
	var arts artifactFlags
	fs.Var(&arts, "artifact", "file:os:arch:kind:url (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *version == "" || len(arts) == 0 {
		return fmt.Errorf("-version and at least one -artifact are required")
	}
	m := &updater.Manifest{
		Version: *version, Channel: *channel,
		PublishedAt: time.Now().UTC(), NotesURL: *notesURL,
	}
	m.RenewFreshness(time.Now())
	for _, spec := range arts {
		parts := strings.SplitN(spec, ":", 5)
		if len(parts) != 5 {
			return fmt.Errorf("artifact %q: want file:os:arch:kind:url", spec)
		}
		file, goos, goarch, kind, url := parts[0], parts[1], parts[2], parts[3], parts[4]
		sum, size, err := hashFile(file)
		if err != nil {
			return err
		}
		name := file[strings.LastIndexAny(file, `/\`)+1:]
		m.Artifacts = append(m.Artifacts, updater.Artifact{
			OS: goos, Arch: goarch, Kind: kind, URL: url,
			SHA256: sum, Size: size, Filename: name,
		})
	}
	if err := m.Validate(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if *out == "" {
		fmt.Println(string(b))
		return nil
	}
	return os.WriteFile(*out, append(b, '\n'), 0o644)
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
