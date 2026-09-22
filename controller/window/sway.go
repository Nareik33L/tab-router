package window

import (
	"encoding/json"
	"fmt"
)

type swayOutput struct {
	Name    string  `json:"name"`
	Primary bool    `json:"primary"`
	Active  bool    `json:"active"`
	Scale   float64 `json:"scale"`
	Rect    struct {
		X      int `json:"x"`
		Y      int `json:"y"`
		Width  int `json:"width"`
		Height int `json:"height"`
	} `json:"rect"`
}

func parseSway(raw []byte) ([]Display, error) {
	var outputs []swayOutput
	if err := json.Unmarshal(raw, &outputs); err != nil {
		return nil, err
	}
	var list []Display
	for _, o := range outputs {
		if !o.Active || o.Rect.Width < 1 || o.Rect.Height < 1 {
			continue
		}
		scale := o.Scale
		if scale <= 0 {
			scale = 1
		}
		r := Rect{X: o.Rect.X, Y: o.Rect.Y, Width: o.Rect.Width, Height: o.Rect.Height}
		list = append(list, Display{
			ID: o.Name, Name: o.Name, Primary: o.Primary, Scale: scale, Bounds: r, Work: r,
		})
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("no active sway outputs")
	}
	if !anyPrimary(list) {
		list[0].Primary = true
	}
	return list, nil
}
