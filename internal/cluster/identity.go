package cluster

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// NodeFile holds this node's identity in the data directory. It is
// node-local: never copy it to another node (two nodes with one ID would
// overwrite each other's heartbeat).
const NodeFile = "node.json"

// Identity names this node within a cluster.
type Identity struct {
	// NodeID is generated on first run and persisted in NodeFile.
	NodeID string `json:"nodeId"`
	// Name is the display name: NodeFile's "name" when the owner set one,
	// else the hostname.
	Name string `json:"name,omitempty"`
}

// LoadIdentity reads dataDir/node.json, creating it with a fresh node ID on
// first run. A corrupt file is an error rather than silently replaced, as a
// new ID would make the node a different cluster member.
func LoadIdentity(dataDir string) (Identity, error) {
	path := filepath.Join(dataDir, NodeFile)
	var id Identity
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(b, &id); err != nil {
			return Identity{}, fmt.Errorf("cluster: %s: %w", path, err)
		}
	case errors.Is(err, fs.ErrNotExist):
	default:
		return Identity{}, fmt.Errorf("cluster: read %s: %w", path, err)
	}
	id.NodeID = strings.TrimSpace(id.NodeID)
	if id.NodeID == "" {
		id.NodeID = newID()
		if err := writeIdentity(path, Identity{NodeID: id.NodeID, Name: strings.TrimSpace(id.Name)}); err != nil {
			return Identity{}, err
		}
	}
	id.Name = strings.TrimSpace(id.Name)
	if id.Name == "" {
		id.Name = hostname()
	}
	return id, nil
}

func writeIdentity(path string, id Identity) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("cluster: %w", err)
	}
	b, err := json.MarshalIndent(id, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("cluster: write %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("cluster: write %s: %w", path, err)
	}
	return nil
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return hex.EncodeToString(b[:])
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil || strings.TrimSpace(h) == "" {
		return "ether"
	}
	// Drop a domain suffix (".local" on macOS): the short name reads better.
	if i := strings.IndexByte(h, '.'); i > 0 {
		h = h[:i]
	}
	return h
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
