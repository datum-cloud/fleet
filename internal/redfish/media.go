package redfish

import (
	"context"
	"fmt"
	"strings"

	"github.com/stmcginnis/gofish/schemas"
)

// Media is one virtual media device of a System, as the BMC reports it.
// Image is the URL the BMC holds, which can carry a capability: callers
// compare it by digest and never record it.
type Media struct {
	Path       string   `json:"path"`
	ID         string   `json:"id"`
	MediaTypes []string `json:"mediaTypes"`
	Inserted   bool     `json:"inserted"`
	Image      string   `json:"-"`
	insert     string
	eject      string
}

// CanInsert and CanEject report whether the device offers each action.
func (m Media) CanInsert() bool { return m.insert != "" }
func (m Media) CanEject() bool  { return m.eject != "" }

// Empty reports whether the device holds no medium.
func (m Media) Empty() bool { return !m.Inserted && m.Image == "" }

// rawMedia holds the properties and action targets read from a VirtualMedia
// resource. Only the fields Fleet uses are decoded.
type rawMedia struct {
	ID         string   `json:"Id"`
	MediaTypes []string `json:"MediaTypes"`
	Inserted   bool     `json:"Inserted"`
	Image      *string  `json:"Image"`
	Actions    struct {
		Insert struct {
			Target string `json:"target"`
		} `json:"#VirtualMedia.InsertMedia"`
		Eject struct {
			Target string `json:"target"`
		} `json:"#VirtualMedia.EjectMedia"`
	} `json:"Actions"`
}

// VirtualMedia reads every virtual media device of the retained System. The
// collection must be the System's own, and each device and action target
// must sit inside it, so a link the BMC returns cannot direct an insert to
// another System. It issues GETs only.
func (c *Client) VirtualMedia(ctx context.Context, sys System) ([]Media, error) {
	api, err := c.connect(ctx)
	if err != nil {
		return nil, err
	}
	var system struct {
		ID           string
		UUID         string
		VirtualMedia struct {
			ID string `json:"@odata.id"`
		}
	}
	if err := getJSON(api, sys.Path, &system); err != nil {
		return nil, err
	}
	if system.ID != sys.ID || !strings.EqualFold(system.UUID, sys.UUID) {
		return nil, fmt.Errorf("%w: retained System path changed identity", ErrIdentity)
	}
	if system.VirtualMedia.ID == "" {
		return nil, fmt.Errorf("%w: the System has no VirtualMedia collection", ErrUnsupported)
	}
	collection, err := cleanPath(system.VirtualMedia.ID)
	if err != nil || !strings.HasPrefix(collection, sys.Path+"/") {
		return nil, fmt.Errorf("%w: VirtualMedia collection not on the enrolled System", ErrIdentity)
	}
	var members struct {
		Members []struct {
			ID string `json:"@odata.id"`
		}
	}
	if err := getJSON(api, collection, &members); err != nil {
		return nil, err
	}
	var out []Media
	for _, member := range members.Members {
		path, err := cleanPath(member.ID)
		if err != nil || !strings.HasPrefix(path, collection+"/") {
			return nil, fmt.Errorf("%w: virtual media device outside the collection", ErrIdentity)
		}
		var raw rawMedia
		if err := getJSON(api, path, &raw); err != nil {
			return nil, err
		}
		media := Media{Path: path, ID: raw.ID, MediaTypes: raw.MediaTypes, Inserted: raw.Inserted}
		if raw.Image != nil {
			media.Image = *raw.Image
		}
		for target, into := range map[string]*string{raw.Actions.Insert.Target: &media.insert, raw.Actions.Eject.Target: &media.eject} {
			if target == "" {
				continue
			}
			clean, err := cleanPath(target)
			if err != nil || !strings.HasPrefix(clean, path+"/") {
				return nil, fmt.Errorf("%w: virtual media action not on its device", ErrIdentity)
			}
			*into = clean
		}
		out = append(out, media)
	}
	return out, nil
}

// InsertMedia posts exactly one InsertMedia to the device: the image over
// HTTPS, write protected. HTTP success is not evidence of the effect;
// callers read the device back.
func (c *Client) InsertMedia(ctx context.Context, media Media, image string) error {
	if media.insert == "" {
		return fmt.Errorf("%w: InsertMedia on %s", ErrUnsupported, media.ID)
	}
	if !strings.HasPrefix(image, "https://") {
		return fmt.Errorf("%w: virtual media is inserted only over HTTPS", ErrUnsupported)
	}
	api, err := c.connect(ctx)
	if err != nil {
		return err
	}
	body := map[string]any{"Image": image, "Inserted": true, "WriteProtected": true, "TransferProtocolType": "HTTPS"}
	resp, err := api.PostWithHeaders(media.insert, body, nil)
	schemas.DeferredCleanupHTTPResponse(resp)
	return classify(err)
}

// EjectMedia posts exactly one EjectMedia to the device. Callers read the
// device back.
func (c *Client) EjectMedia(ctx context.Context, media Media) error {
	if media.eject == "" {
		return fmt.Errorf("%w: EjectMedia on %s", ErrUnsupported, media.ID)
	}
	api, err := c.connect(ctx)
	if err != nil {
		return err
	}
	resp, err := api.PostWithHeaders(media.eject, map[string]any{}, nil)
	schemas.DeferredCleanupHTTPResponse(resp)
	return classify(err)
}
