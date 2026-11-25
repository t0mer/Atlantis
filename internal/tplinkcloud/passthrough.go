package tplinkcloud

import (
	"context"
	"encoding/json"
	"errors"
)

// Passthrough relays request to a device through the cloud and returns the
// device's responseData. appServerURL and deviceID come from a Site. The ATA
// transport (internal/deco/tmp/ata) builds the inner request.
func (c *Client) Passthrough(ctx context.Context, appServerURL, deviceID string, request any) (json.RawMessage, error) {
	if appServerURL == "" || deviceID == "" {
		return nil, errors.New("tplinkcloud: passthrough needs a server URL and device ID")
	}
	var res struct {
		ResponseData json.RawMessage `json:"responseData"`
	}
	err := c.call(ctx, appServerURL, "/api/v2/common/passthrough", map[string]any{
		"deviceId":    deviceID,
		"requestData": request,
	}, &res)
	if err != nil {
		return nil, err
	}
	if len(res.ResponseData) == 0 || string(res.ResponseData) == "null" {
		return nil, errors.New("tplinkcloud: passthrough returned no responseData")
	}
	return res.ResponseData, nil
}
