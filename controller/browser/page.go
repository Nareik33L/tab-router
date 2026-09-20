package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Page is an attached page target (one tab).
type Page struct {
	b         *Browser
	TargetID  string
	SessionID string
}

type targetInfo struct {
	TargetID string `json:"targetId"`
	Type     string `json:"type"`
	URL      string `json:"url"`
	Attached bool   `json:"attached"`
}

// FirstPage attaches to the initial about:blank tab Chromium opened.
func (b *Browser) FirstPage(ctx context.Context) (*Page, error) {
	deadline := time.Now().Add(15 * time.Second)
	for {
		var res struct {
			TargetInfos []targetInfo `json:"targetInfos"`
		}
		if err := b.cdp.Call(ctx, "", "Target.getTargets", nil, &res); err != nil {
			return nil, err
		}
		for _, t := range res.TargetInfos {
			if t.Type == "page" {
				return b.attach(ctx, t.TargetID)
			}
		}
		if time.Now().After(deadline) {
			return nil, errors.New("browser: no page target appeared")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// NewPage opens a new tab (in the identity's window) at about:blank.
func (b *Browser) NewPage(ctx context.Context) (*Page, error) {
	var res struct {
		TargetID string `json:"targetId"`
	}
	if err := b.cdp.Call(ctx, "", "Target.createTarget", map[string]any{"url": "about:blank"}, &res); err != nil {
		return nil, err
	}
	return b.attach(ctx, res.TargetID)
}

func (b *Browser) attach(ctx context.Context, targetID string) (*Page, error) {
	var res struct {
		SessionID string `json:"sessionId"`
	}
	if err := b.cdp.Call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": targetID, "flatten": true}, &res); err != nil {
		return nil, err
	}
	p := &Page{b: b, TargetID: targetID, SessionID: res.SessionID}
	for _, m := range []string{"Page.enable", "Network.enable", "Runtime.enable"} {
		if err := b.cdp.Call(ctx, p.SessionID, m, nil, nil); err != nil {
			return nil, err
		}
	}
	_ = p.waitReady(ctx)
	return p, nil
}

// waitReady waits until the tab has finished its initial about:blank load.
// Navigating before that, especially on Windows Chromium, yields
// net::ERR_ABORTED and no proxy CONNECT.
func (p *Page) waitReady(ctx context.Context) error {
	deadline, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		var state string
		if err := p.Evaluate(deadline, "document.readyState", &state); err == nil && (state == "complete" || state == "interactive") {
			return nil
		}
		select {
		case <-deadline.Done():
			return nil
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// Close closes the tab.
func (p *Page) Close(ctx context.Context) error {
	return p.b.cdp.Call(ctx, "", "Target.closeTarget", map[string]any{"targetId": p.TargetID}, nil)
}

// NavResult describes the outcome of a navigation.
type NavResult struct {
	RequestedURL string
	FinalURL     string
	// ErrorText is Chromium's net error (e.g. net::ERR_PROXY_CONNECTION_FAILED)
	// when the navigation failed before a response.
	ErrorText string
	Status    int
	Duration  time.Duration
}

// Blocked reports whether the navigation failed at the network layer.
func (r NavResult) Blocked() bool { return r.ErrorText != "" }

// Navigate loads url and waits for the load event (or a network error).
func (p *Page) Navigate(ctx context.Context, url string) (NavResult, error) {
	var last NavResult
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		last, lastErr = p.navigateOnce(ctx, url)
		if lastErr != nil {
			return last, lastErr
		}
		if !last.Blocked() || !transientNavError(last.ErrorText) {
			return last, nil
		}
		select {
		case <-ctx.Done():
			return last, nil
		case <-time.After(200 * time.Millisecond):
		}
	}
	return last, lastErr
}

func transientNavError(s string) bool {
	// ERR_FAILED is how Chromium reports a reset keep-alive after we tore
	// down gate tunnels; the next attempt usually succeeds.
	return strings.Contains(s, "ERR_ABORTED") || strings.Contains(s, "ERR_FAILED")
}

func (p *Page) navigateOnce(ctx context.Context, url string) (NavResult, error) {
	start := time.Now()
	res := NavResult{RequestedURL: url}
	events, cancel := p.b.cdp.Subscribe(p.SessionID, "")
	defer cancel()

	var nav struct {
		FrameID   string `json:"frameId"`
		LoaderID  string `json:"loaderId"`
		ErrorText string `json:"errorText"`
	}
	if err := p.b.cdp.Call(ctx, p.SessionID, "Page.navigate", map[string]any{"url": url}, &nav); err != nil {
		return res, err
	}
	if nav.ErrorText != "" {
		res.ErrorText = nav.ErrorText
		res.Duration = time.Since(start)
		return res, nil
	}
	var docErr string
	finish := func() NavResult {
		res.Duration = time.Since(start)
		if res.FinalURL == "" {
			res.FinalURL, _ = p.URL(ctx)
		}
		if res.ErrorText == "" && isErrorPageURL(res.FinalURL) {
			res.ErrorText = firstNonEmpty(docErr, "net::ERR_FAILED")
		}
		return res
	}
	for {
		select {
		case <-ctx.Done():
			return res, fmt.Errorf("navigate %s: %w", url, ctx.Err())
		case ev, ok := <-events:
			if !ok {
				return res, ErrClosed
			}
			switch ev.Method {
			case "Network.responseReceived":
				var r struct {
					FrameID  string `json:"frameId"`
					LoaderID string `json:"loaderId"`
					Type     string `json:"type"`
					Response struct {
						URL    string `json:"url"`
						Status int    `json:"status"`
					} `json:"response"`
				}
				if json.Unmarshal(ev.Params, &r) == nil && r.Type == "Document" && r.FrameID == nav.FrameID && r.LoaderID == nav.LoaderID {
					res.FinalURL = r.Response.URL
					res.Status = r.Response.Status
				}
			case "Network.loadingFailed":
				var f struct {
					ErrorText string `json:"errorText"`
					Type      string `json:"type"`
					Canceled  bool   `json:"canceled"`
				}
				if json.Unmarshal(ev.Params, &f) == nil && f.Type == "Document" && f.ErrorText != "" {
					// A canceled failure is usually the previous document
					// being aborted for this navigate. Keep waiting for the
					// new load (or an error page).
					if f.Canceled {
						docErr = f.ErrorText
						break
					}
					res.ErrorText = f.ErrorText
					return finish(), nil
				}
			case "Page.frameNavigated":
				var f struct {
					Frame struct {
						ID  string `json:"id"`
						URL string `json:"url"`
					} `json:"frame"`
				}
				if json.Unmarshal(ev.Params, &f) == nil && f.Frame.ID == nav.FrameID {
					res.FinalURL = f.Frame.URL
				}
			case "Page.loadEventFired":
				return finish(), nil
			}
		}
	}
}

func isErrorPageURL(u string) bool {
	u = strings.ToLower(u)
	return strings.HasPrefix(u, "chrome-error:") ||
		strings.HasPrefix(u, "chrome://network-error") ||
		strings.Contains(u, "chromewebdata")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// URL returns the page's current URL.
func (p *Page) URL(ctx context.Context) (string, error) {
	var s string
	if err := p.Evaluate(ctx, "location.href", &s); err != nil {
		return "", err
	}
	return s, nil
}

// Evaluate runs expr and unmarshals the by-value result into out.
func (p *Page) Evaluate(ctx context.Context, expr string, out any) error {
	var res struct {
		Result struct {
			Type  string          `json:"type"`
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text      string `json:"text"`
			Exception *struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	params := map[string]any{"expression": expr, "returnByValue": true, "awaitPromise": true}
	if err := p.b.cdp.Call(ctx, p.SessionID, "Runtime.evaluate", params, &res); err != nil {
		return err
	}
	if res.ExceptionDetails != nil {
		msg := res.ExceptionDetails.Text
		if res.ExceptionDetails.Exception != nil {
			msg = res.ExceptionDetails.Exception.Description
		}
		return fmt.Errorf("evaluate: %s", msg)
	}
	if out == nil || len(res.Result.Value) == 0 {
		return nil
	}
	return json.Unmarshal(res.Result.Value, out)
}

// BodyText returns document.body.innerText.
func (p *Page) BodyText(ctx context.Context) (string, error) {
	var s string
	err := p.Evaluate(ctx, "document.body ? document.body.innerText : ''", &s)
	return s, err
}

// Cookie is the subset of CDP Network.Cookie the verifier uses.
type Cookie struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Domain string `json:"domain"`
	Path   string `json:"path"`
}

// SetCookie stores a cookie for url in this browser's profile.
func (p *Page) SetCookie(ctx context.Context, url, name, value string) error {
	return p.setCookie(ctx, url, name, value, 0)
}

// SetPersistentCookie sets a cookie that survives browser restarts.
func (p *Page) SetPersistentCookie(ctx context.Context, url, name, value string, ttl time.Duration) error {
	return p.setCookie(ctx, url, name, value, ttl)
}

func (p *Page) setCookie(ctx context.Context, url, name, value string, ttl time.Duration) error {
	var res struct {
		Success bool `json:"success"`
	}
	params := map[string]any{"url": url, "name": name, "value": value}
	if ttl > 0 {
		params["expires"] = float64(time.Now().Add(ttl).Unix())
	}
	err := p.b.cdp.Call(ctx, p.SessionID, "Network.setCookie", params, &res)
	if err != nil {
		return err
	}
	if !res.Success {
		return errors.New("setCookie: rejected")
	}
	return nil
}

// GetCookies returns cookies visible for the given URLs.
func (p *Page) GetCookies(ctx context.Context, urls ...string) ([]Cookie, error) {
	var res struct {
		Cookies []Cookie `json:"cookies"`
	}
	params := map[string]any{}
	if len(urls) > 0 {
		params["urls"] = urls
	}
	if err := p.b.cdp.Call(ctx, p.SessionID, "Network.getCookies", params, &res); err != nil {
		return nil, err
	}
	return res.Cookies, nil
}

// DeleteCookie removes a cookie set for url.
func (p *Page) DeleteCookie(ctx context.Context, url, name string) error {
	return p.b.cdp.Call(ctx, p.SessionID, "Network.deleteCookies", map[string]any{"name": name, "url": url}, nil)
}
