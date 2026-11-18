package gh

import (
	"context"
	"fmt"
	"io"
)

// Release is a published GitHub release.
type Release struct {
	Tag    string  `json:"tag_name"`
	Notes  string  `json:"body"`
	URL    string  `json:"html_url"`
	Assets []Asset `json:"assets"`
}

// Asset is a file attached to a release.
type Asset struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// LatestRelease is repo's newest release that is neither a draft nor a
// pre-release.
func (c *Client) LatestRelease(ctx context.Context, repo string) (Release, error) {
	var r Release
	if err := c.rest.DoWithContext(ctx, "GET", "repos/"+repo+"/releases/latest", nil, &r); err != nil {
		return Release{}, fmt.Errorf("latest release of %s: %w", repo, err)
	}
	return r, nil
}

// Download fetches the release asset called name. It goes through the API
// with your login, so it works for a private repo too.
func (c *Client) Download(ctx context.Context, repo string, r Release, name string) ([]byte, error) {
	for _, a := range r.Assets {
		if a.Name != name {
			continue
		}
		resp, err := c.asset.RequestWithContext(ctx, "GET", fmt.Sprintf("repos/%s/releases/assets/%d", repo, a.ID), nil)
		if err != nil {
			return nil, fmt.Errorf("download %s: %w", name, err)
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(io.LimitReader(resp.Body, maxAsset))
		if err != nil {
			return nil, fmt.Errorf("download %s: %w", name, err)
		}
		return b, nil
	}
	return nil, fmt.Errorf("%s has no %s", r.Tag, name)
}

// maxAsset bounds a download; prowl's archives are a few megabytes.
const maxAsset = 256 << 20
