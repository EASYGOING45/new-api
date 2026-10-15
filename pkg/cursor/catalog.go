package cursor

import (
	"context"
	"errors"
	"time"
)

// Catalog coalesces concurrent discovery per User API Key. Expired snapshots
// and failed refreshes are not served as proof of account availability.
func (c *Client) Catalog(ctx context.Context, key string, force bool) (*Catalog, error) {
	a, err := c.account(key)
	if err != nil {
		return nil, err
	}
	for {
		a.mu.Lock()
		if a.catalogLoading != nil {
			done := a.catalogLoading
			a.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, errors.New("Cursor model catalog request cancelled")
			case <-done:
				force = false
				continue
			}
		}
		if c.now().Before(a.catalogRetryAt) && a.catalogError != nil {
			err := a.catalogError
			a.mu.Unlock()
			return nil, err
		}
		if !force && a.catalog != nil && c.now().Before(a.catalog.ExpiresAt) {
			result := a.catalog
			a.mu.Unlock()
			return result, nil
		}
		a.catalogLoading = make(chan struct{})
		a.mu.Unlock()
		result, err := c.fetchCatalog(ctx, a, key)
		a.mu.Lock()
		if err == nil {
			a.catalog = result
			a.catalogError = nil
			a.catalogRetryAt = time.Time{}
		} else {
			a.catalog = nil
			a.catalogError = err
			// Bound repeated failures without using an obsolete capability table.
			a.catalogRetryAt = c.now().Add(10 * time.Second)
		}
		close(a.catalogLoading)
		a.catalogLoading = nil
		a.mu.Unlock()
		return result, err
	}
}

func (c *Client) fetchCatalog(parent context.Context, a *account, key string) (*Catalog, error) {
	ctx, cancel := context.WithTimeout(parent, 25*time.Second)
	defer cancel()
	token, _, status := c.authenticate(ctx, a, key)
	if status != 200 {
		return nil, errors.New("Cursor model catalog: " + statusMessage(status))
	}
	picker, status, err := c.postJSON(ctx, "/aiserver.v1.AiService/AvailableModels", token, `{"useModelParameters":true,"doNotUseMarkdown":true}`, 8<<20)
	if err == nil {
		var usable []byte
		usable, status, err = c.postJSON(ctx, "/agent.v1.AgentService/GetUsableModels", token, `{}`, 8<<20)
		if err == nil {
			return parseCatalog(picker, usable, c.now())
		}
	}
	if status == 401 {
		a.mu.Lock()
		a.token = ""
		a.expires = time.Time{}
		a.mu.Unlock()
	}
	return nil, errors.New("Cursor model catalog: " + statusMessage(status))
}
