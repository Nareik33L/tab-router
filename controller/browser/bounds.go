package browser

import "context"

// WindowBounds is a Chromium window's position, size, and show-state.
// Coordinates are the pixels Browser.setWindowBounds uses.
type WindowBounds struct {
	X, Y, Width, Height int
	// State is "normal", "minimized", "maximized", or "fullscreen".
	State string
}

// WindowBounds reads the bounds of the window that contains targetID.
func (b *Browser) WindowBounds(ctx context.Context, targetID string) (WindowBounds, error) {
	id, err := b.windowID(ctx, targetID)
	if err != nil {
		return WindowBounds{}, err
	}
	var res struct {
		Bounds struct {
			Left        int    `json:"left"`
			Top         int    `json:"top"`
			Width       int    `json:"width"`
			Height      int    `json:"height"`
			WindowState string `json:"windowState"`
		} `json:"bounds"`
	}
	if err := b.cdp.Call(ctx, "", "Browser.getWindowBounds", map[string]any{"windowId": id}, &res); err != nil {
		return WindowBounds{}, err
	}
	return WindowBounds{
		X: res.Bounds.Left, Y: res.Bounds.Top,
		Width: res.Bounds.Width, Height: res.Bounds.Height,
		State: res.Bounds.WindowState,
	}, nil
}

// SetWindowBounds moves and resizes the window that contains targetID.
// It does not move the pointer.
func (b *Browser) SetWindowBounds(ctx context.Context, targetID string, bounds WindowBounds) error {
	id, err := b.windowID(ctx, targetID)
	if err != nil {
		return err
	}
	payload := map[string]any{
		"left": bounds.X, "top": bounds.Y,
		"width": bounds.Width, "height": bounds.Height,
	}
	if bounds.State != "" {
		payload["windowState"] = bounds.State
	}
	return b.cdp.Call(ctx, "", "Browser.setWindowBounds", map[string]any{
		"windowId": id,
		"bounds":   payload,
	}, nil)
}

// FocusWindow activates the page without moving the pointer or any other window.
func (b *Browser) FocusWindow(ctx context.Context, targetID, sessionID string) error {
	if err := b.cdp.Call(ctx, "", "Target.activateTarget", map[string]any{"targetId": targetID}, nil); err != nil {
		return err
	}
	if sessionID != "" {
		return b.cdp.Call(ctx, sessionID, "Page.bringToFront", nil, nil)
	}
	return nil
}

func (b *Browser) windowID(ctx context.Context, targetID string) (int, error) {
	var res struct {
		WindowID int `json:"windowId"`
	}
	params := map[string]any{}
	if targetID != "" {
		params["targetId"] = targetID
	}
	if err := b.cdp.Call(ctx, "", "Browser.getWindowForTarget", params, &res); err != nil {
		return 0, err
	}
	return res.WindowID, nil
}
