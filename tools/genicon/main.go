// 生成观澜应用图标：深蓝渐变圆角底 + 三条水波线（观水之澜）。
// 用法: go run ./tools/genicon  （输出到 pkg/guanlan 与 app/ui/images）
package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

type wave struct {
	amp, freq, phase, base float64
	col                     color.RGBA
}

func main() {
	root := "pkg/guanlan"
	if len(os.Args) > 1 { // 仓库根（相对当前目录），例如: go run . ../..
		root = filepath.Join(os.Args[1], "pkg/guanlan")
	}
	sizes := []struct {
		path string
		size int
	}{
		{filepath.Join(root, "ICON.PNG"), 512},
		{filepath.Join(root, "ICON_256.PNG"), 256},
		{filepath.Join(root, "app", "ui", "images", "icon_256.png"), 256},
		{filepath.Join(root, "app", "ui", "images", "icon_64.png"), 64},
	}
	for _, s := range sizes {
		if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
			panic(err)
		}
		f, err := os.Create(s.path)
		if err != nil {
			panic(err)
		}
		png.Encode(f, render(s.size))
		f.Close()
		fmt.Println("生成", s.path)
	}
}

func render(size int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	r := size * 96 / 512 // 圆角半径
	waves := []wave{
		{amp: 0.075, freq: 2.0, phase: 0.0, base: 0.42, col: color.RGBA{57, 197, 207, 255}},  // 青
		{amp: 0.055, freq: 2.6, phase: 1.2, base: 0.53, col: color.RGBA{47, 129, 247, 255}},  // 蓝
		{amp: 0.035, freq: 3.2, phase: 2.4, base: 0.63, col: color.RGBA{63, 185, 80, 255}},   // 绿
	}
	thick := size * 20 / 512 // 波线粗细（直径）

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			if !inRounded(x, y, size, r) {
				continue
			}
			img.Set(x, y, bg(y, size))
		}
	}
	for _, w := range waves {
		A := w.amp * float64(size)
		base := w.base * float64(size)
		half := thick / 2
		for x := 0; x < size; x++ {
			u := float64(x) / float64(size)
			yy := base + A*math.Sin(2*math.Pi*w.freq*u+w.phase)
			yi := int(yy)
			for d := -half; d <= half; d++ {
				y := yi + d
				if y < 0 || y >= size {
					continue
				}
				if !inRounded(x, y, size, r) {
					continue
				}
				img.Set(x, y, w.col)
			}
		}
	}
	return img
}

// inRounded 点是否在圆角矩形内
func inRounded(x, y, size, r int) bool {
	if x >= r && x < size-r {
		return true
	}
	if y >= r && y < size-r {
		return true
	}
	cx := clamp(x, r, size-1-r)
	cy := clamp(y, r, size-1-r)
	dx, dy := x-cx, y-cy
	return dx*dx+dy*dy <= r*r
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// bg 垂直渐变：#0d1b2a → #16324f
func bg(y, size int) color.RGBA {
	t := float64(y) / float64(size)
	return color.RGBA{
		R: uint8(13 + t*(22-13)),
		G: uint8(27 + t*(50-27)),
		B: uint8(42 + t*(79-42)),
		A: 255,
	}
}
