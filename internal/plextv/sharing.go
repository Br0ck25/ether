package plextv

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

func marshalShare(machine, email string, sections []int) ([]byte, error) {
	return json.Marshal(map[string]any{"server_id": machine, "shared_server": map[string]any{"library_section_ids": sections, "invited_email": email}, "sharing_settings": map[string]string{"allowSync": "0", "allowCameraUpload": "0", "allowChannels": "0"}})
}

// ShareWith shares Ether's library sections without enabling paid entitlements.
func (c *Client) ShareWith(ctx context.Context, token, email string, sections []int) error {
	if strings.TrimSpace(email) == "" || len(sections) == 0 {
		return fmt.Errorf("email and library sections are required")
	}
	body, err := marshalShare(c.id.ClientIdentifier, email, sections)
	if err != nil {
		return err
	}
	_, err = c.do(ctx, request{method: http.MethodPost, path: "/api/servers/" + url.PathEscape(c.id.ClientIdentifier) + "/shared_servers", token: token, body: body, contentType: "application/json"})
	return err
}
