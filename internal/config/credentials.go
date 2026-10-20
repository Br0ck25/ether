package config

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const CredentialKeyFile = ".credentials-key"
const sealedPrefix = "ether-sealed:v1:"

func credentialKey(dir string, existing []byte) ([]byte, error) {
	path := filepath.Join(dir, CredentialKeyFile)
	key, err := os.ReadFile(path)
	if err == nil {
		if len(key) != 32 {
			return nil, errors.New("invalid credential key")
		}
		return key, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	if strings.Contains(string(existing), sealedPrefix) {
		return nil, errors.New("encrypted credentials require the original credential key; restore the complete backup")
	}
	key = make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		return credentialKey(dir, existing)
	}
	if err != nil {
		return nil, err
	}
	_, err = f.Write(key)
	if err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err != nil {
		return nil, err
	}
	if ce != nil {
		return nil, ce
	}
	return key, nil
}
func secretField(k string) bool {
	switch strings.ToLower(k) {
	case "token", "apikey", "password", "clientsecret", "username":
		return true
	}
	return false
}
func transformCredentials(data, key []byte, seal bool) ([]byte, error) {
	var root any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	var walk func(any) error
	walk = func(v any) error {
		switch x := v.(type) {
		case map[string]any:
			for k, value := range x {
				if inner, ok := value.(map[string]any); ok && k == "secretSettings" {
					// Every value of a secret-settings map is a credential.
					if err := sealAll(inner, aead, seal); err != nil {
						return err
					}
					continue
				}
				if text, ok := value.(string); ok && text != "" && secretField(k) {
					if seal {
						nonce := make([]byte, aead.NonceSize())
						if _, err := rand.Read(nonce); err != nil {
							return err
						}
						payload := aead.Seal(nonce, nonce, []byte(text), []byte(k))
						x[k] = sealedPrefix + base64.RawStdEncoding.EncodeToString(payload)
					} else if strings.HasPrefix(text, sealedPrefix) {
						payload, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(text, sealedPrefix))
						if err != nil || len(payload) < aead.NonceSize() {
							return errors.New("invalid sealed credential")
						}
						plain, err := aead.Open(nil, payload[:aead.NonceSize()], payload[aead.NonceSize():], []byte(k))
						if err != nil {
							return fmt.Errorf("decrypt credential %s: key mismatch or damaged configuration", k)
						}
						x[k] = string(plain)
					}
				} else if err := walk(value); err != nil {
					return err
				}
			}
		case []any:
			for _, value := range x {
				if err := walk(value); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(root); err != nil {
		return nil, err
	}
	return json.MarshalIndent(root, "", "  ")
}

// sealAll seals or opens every non-empty string value of m.
func sealAll(m map[string]any, aead cipher.AEAD, seal bool) error {
	for k, value := range m {
		text, ok := value.(string)
		if !ok || text == "" {
			continue
		}
		if seal {
			nonce := make([]byte, aead.NonceSize())
			if _, err := rand.Read(nonce); err != nil {
				return err
			}
			m[k] = sealedPrefix + base64.RawStdEncoding.EncodeToString(aead.Seal(nonce, nonce, []byte(text), []byte(k)))
			continue
		}
		if !strings.HasPrefix(text, sealedPrefix) {
			continue
		}
		payload, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(text, sealedPrefix))
		if err != nil || len(payload) < aead.NonceSize() {
			return errors.New("invalid sealed credential")
		}
		plain, err := aead.Open(nil, payload[:aead.NonceSize()], payload[aead.NonceSize():], []byte(k))
		if err != nil {
			return fmt.Errorf("decrypt credential %s: key mismatch or damaged configuration", k)
		}
		m[k] = string(plain)
	}
	return nil
}

func clone(c Config) Config {
	b, _ := json.Marshal(c)
	var out Config
	_ = json.Unmarshal(b, &out)
	return out
}
