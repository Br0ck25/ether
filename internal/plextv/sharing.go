package plextv

import "encoding/json"

func marshalShare(machine, email string, sections []int) ([]byte, error) {
	return json.Marshal(map[string]any{"server_id": machine, "shared_server": map[string]any{"library_section_ids": sections, "invited_email": email}, "sharing_settings": map[string]string{"allowSync": "0", "allowCameraUpload": "0", "allowChannels": "0"}})
}
